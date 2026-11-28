// Package daemon wires pao's node together: the registry, the control plane
// over tmux, the router, the message store, the communication layer, the local
// channel, and a mirror that follows each running agent's transcript and sends
// what the agent says to the human (ADR-0001, ADR-0007, ADR-0008, ADR-0009).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/agenttoken"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	backends "github.com/yaad-index/personal-agent-orchestrator/internal/backend/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/budget"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/defaultagent"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/grants"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/transport"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/rejected"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/runsecret"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/tmux"
	"github.com/yaad-index/personal-agent-orchestrator/internal/upstream"
	"github.com/yaad-index/personal-agent-orchestrator/internal/webui"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

// readers maps a profile's transcript format to its reader. A backend joins
// pao by adding its reader here (ADR-0011).
var readers = map[string]core.TranscriptReader{
	claudecode.Format: claudecode.Reader{},
}

// Options tune a Run; the zero value is the production configuration.
type Options struct {
	// Listener, when set, replaces listening on the configured address.
	Listener net.Listener
	// MirrorPoll is how often transcripts are checked for growth.
	MirrorPoll time.Duration
	// PollInterval is how often startup checks for readiness.
	PollInterval time.Duration
	// PromptSettle is how long a start-up question must be shown before it is
	// answered; zero is control.DefaultSettle.
	PromptSettle time.Duration
	// DispatchEvery is how often open tasks are dispatched; zero is every 5s.
	DispatchEvery time.Duration
	// BudgetReader builds a metered account's reader; nil builds the reader the
	// account's config names.
	BudgetReader func(config.Account) (budget.Reader, error)
	// MeshListener, when set, replaces listening on the configured mesh address.
	MeshListener net.Listener
	// Authorizer is the access seam every surface asks. Nil means the access
	// matrix read from the grants in the state directory.
	Authorizer access.Authorizer
	// Adapters is the table of channel adapters to build configured channels from.
	// Nil means the adapters compiled into this build.
	Adapters adapter.Table
	// Self is the pao binary a task-agent's pane runs to confine it behind the
	// egress plane (ADR-0049). Empty means the node does not confine: a task
	// with a secret to inject is refused and other task-agents run unconfined.
	Self string
	// ConfigPath is the orchestrator config the node was loaded from. Set, the
	// management UI may change the channels of the file pao manages beside it
	// (ADR-0055); empty, it shows and changes none.
	ConfigPath string
	// StartEnv looks a variable up in the environment pao was started with,
	// which a restart starts with again. It tells the UI whether a channel's
	// credential would resolve (ADR-0057 point 4). Nil reads the current one.
	StartEnv func(string) (string, bool)
}

// Run serves the node until ctx is done. token is the local channel's bearer
// token.
func Run(ctx context.Context, cfg *config.Config, token string, opts Options) error {
	// A restart asked from the management UI stops the node as a signal does:
	// everything the node runs is under this context.
	ctx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	// Agents run under a tmux server this process starts, and a session inherits
	// the server's environment. The operator's token must not be in it: an agent
	// could otherwise read it from its own environment and speak as the operator.
	// This keeps it out of what agents inherit; it does not make it unreadable
	// to another process of the same user.
	if name := cfg.Orchestrator.TokenEnv; name != "" {
		_ = os.Unsetenv(name)
	}
	// A secret held in a variable is read once and taken out of the environment
	// for the same reason, so no agent inherits it; only the run that names it is
	// given it, as a file (ADR-0036 point 3), or the server or channel it is
	// bound to.
	secretValues := map[string]string{}
	for _, sec := range cfg.Orchestrator.Secrets {
		if sec.Env != "" {
			secretValues[sec.Name] = os.Getenv(sec.Env)
		}
	}
	for _, sec := range cfg.Orchestrator.Secrets {
		if sec.Env != "" {
			_ = os.Unsetenv(sec.Env)
		}
	}
	// A channel's credential likewise: read once, from its variable or through
	// the secret it names (ADR-0057 point 2), and taken out of the environment.
	creds := providerCredentials(cfg.Orchestrator, secretValues)
	// An upstream MCP server's credential likewise: pao presents it, no agent
	// ever holds it (ADR-0024 point 2). A server's secret is read each time pao
	// connects to it (ADR-0053 point 2).
	var upstreams []*upstream.Server
	for _, u := range cfg.Orchestrator.MCPUpstreams {
		upstreams = append(upstreams, upstream.New(u, upstreamCredential(u, cfg.Orchestrator, secretValues)))
	}
	for _, u := range cfg.Orchestrator.MCPUpstreams {
		if u.CredentialEnv != "" {
			_ = os.Unsetenv(u.CredentialEnv)
		}
	}
	proxy := upstream.NewCatalog(upstreams...)
	defer func() { _ = proxy.Close() }()
	for _, p := range cfg.Profiles {
		if _, ok := readers[p.Transcript.Format]; !ok {
			return fmt.Errorf("agent %q: unknown transcript format %q", p.Name, p.Transcript.Format)
		}
	}

	reg := registry.New()
	configured := configuredIn(reg)
	for _, p := range cfg.Profiles {
		if err := reg.Add(p); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(cfg.Orchestrator.StateDir, 0o700); err != nil {
		return err
	}
	// A node that kept messages has run before; it is what an upgrade starts from.
	ranBefore := exists(filepath.Join(cfg.Orchestrator.StateDir, "messages.db"))
	store, err := msgstore.Open(filepath.Join(cfg.Orchestrator.StateDir, "messages.db"), cfg.Orchestrator.MessageWindow)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	rejects, err := rejected.Open(filepath.Join(cfg.Orchestrator.StateDir, "rejected.db"), rejected.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = rejects.Close() }()
	ids, err := identity.Open(filepath.Join(cfg.Orchestrator.StateDir, "identity.db"))
	if err != nil {
		return err
	}
	defer func() { _ = ids.Close() }()
	if err := rekeyIdentity(ids, cfg.Orchestrator); err != nil {
		return err
	}
	warnUnknownOwners(ids, cfg.Orchestrator.Channels)
	defaults, err := defaultagent.Open(filepath.Join(cfg.Orchestrator.StateDir, defaultsFile))
	if err != nil {
		return err
	}
	grantStore, err := grants.Open(filepath.Join(cfg.Orchestrator.StateDir, grantsFile))
	if err != nil {
		return err
	}
	if err := seedOperator(grantStore, ranBefore, cfg.Profiles, cfg.Orchestrator.RecipesDir); err != nil {
		return err
	}
	auth := opts.Authorizer
	if auth == nil {
		auth = access.Matrix{Grants: grantStore}
	}
	// governorServes says whether the governor is serving a request, set once
	// the layer exists.
	var governorServes func() bool
	governorServing := func() bool { return governorServes != nil && governorServes() }
	if g := cfg.Orchestrator.Governor.Agent; g != "" {
		auth = governorScoped{inner: auth, governor: g, serving: governorServing}
	}

	var ctlOpts []control.Option
	if opts.PromptSettle > 0 {
		ctlOpts = append(ctlOpts, control.WithSettle(opts.PromptSettle))
	}
	if opts.PollInterval > 0 {
		ctlOpts = append(ctlOpts, control.WithPollInterval(opts.PollInterval))
	}
	gateSock := filepath.Join(cfg.Orchestrator.StateDir, "gate.sock")
	// An agent calls pao as itself with a token minted when its session starts
	// and revoked when the session ends; the MCP address is filled in once the
	// node is listening.
	tokens := agenttoken.New()
	// The token reaches the session through a file only the node user can read,
	// never a command line, which any local user can read. A session that
	// outlived the last run of pao still holds its token (ADR-0051 point 3).
	tokenFiles := agentTokenFiles{dir: filepath.Join(cfg.Orchestrator.StateDir, "agent-tokens")}
	if err := tokenFiles.prepare(); err != nil {
		return fmt.Errorf("agent tokens: %w", err)
	}
	var mcpURL atomic.Pointer[string]
	launch, err := backendLaunch(cfg.Orchestrator.Gate.AskTimeout)
	if err != nil {
		return err
	}
	// The MCP server is made after the control plane, which it asks whether an
	// agent is alive. What it keeps for an agent is let go when the agent's
	// session ends.
	var mcpServers atomic.Pointer[mcpsrv.Server]
	forgetMCP := func(agent string) {
		if s := mcpServers.Load(); s != nil {
			s.Forget(access.Agent(agent))
		}
	}
	// A task's secret is put in place for the one run that needs it, and gone
	// when that agent's session ends; at start no run is under way, so whatever
	// is left is swept (ADR-0036 point 3).
	secrets := &runsecret.Store{Dir: filepath.Join(cfg.Orchestrator.StateDir, "run-secrets"), Resolve: resolveSecret(cfg.Orchestrator, secretValues)}
	if err := secrets.Sweep(); err != nil {
		return fmt.Errorf("sweep run secrets: %w", err)
	}
	// Every task-agent runs behind the egress plane where the node can confine
	// it (ADR-0049); pao's MCP is also served on a socket, so a confined agent
	// reaches it through its own network's forward.
	mcpSock := filepath.Join(cfg.Orchestrator.StateDir, "mcp.sock")
	egressPl := newEgressPlane(cfg.Orchestrator, opts.Self, core.SecretResolverFunc(resolveSecret(cfg.Orchestrator, secretValues)), gateSock, mcpSock, backends.Table().Pass)
	if err := egressPl.sweep(); err != nil {
		return fmt.Errorf("sweep egress state: %w", err)
	}
	if egressPl.why != nil {
		slog.Warn("task-agents will not be confined behind the egress plane", "reason", egressPl.why)
	}
	ctlOpts = append(ctlOpts,
		control.WithEnv(func(agent string) []string {
			if err := tokenFiles.write(agent, tokens.Mint(agent)); err != nil {
				slog.Error("could not hand an agent its token; its calls to pao will be refused", "agent", agent, "error", err)
			}
			env := []string{"PAO_AGENT=" + agent, "PAO_GATE_SOCKET=" + gateSock}
			if u := mcpURL.Load(); u != nil {
				env = append(env, backend.EnvMCPURL+"="+*u)
			}
			return append(env, secrets.Env(agent)...)
		}),
		control.WithCommand(func(p core.Profile) (string, []string, error) {
			// The node's profile defaults apply to every agent, a temporary one's
			// included, where its profile does not give its own (#179).
			p = p.WithToolDefaults(cfg.Orchestrator.ProfileDefaults.Tools)
			s, err := backends.Table().Command(p, launch)
			if err != nil {
				return "", nil, err
			}
			command, env, err := egressPl.wrap(p, s.Command, s.Env)
			if err != nil {
				return "", nil, err
			}
			return tokenFiles.load(p.Name, command), env, nil
		}),
		// A backend's start-up question, such as Claude Code's whether to trust
		// the working directory, is answered for an agent the operator configured,
		// whose workdir is the operator's own, and for a temporary agent, whose
		// workdir's settings its adapter shuts out (ADR-0033). Any other agent's
		// question is left for a person.
		control.WithPrompts(func(p core.Profile) []control.Prompt {
			if !configured(p.Name) && !p.Provisioned {
				return nil
			}
			var out []control.Prompt
			for _, q := range backends.Table().Prompts(p) {
				out = append(out, control.Prompt{About: q.About, Pattern: q.Pattern, Keys: q.Keys, Tries: q.Tries})
			}
			return out
		}),
		control.WithOnStop(func(agent string) {
			tokens.Revoke(agent)
			if err := tokenFiles.remove(agent); err != nil {
				slog.Warn("could not remove an agent's token", "agent", agent, "error", err)
			}
			forgetMCP(agent)
			if err := secrets.Wipe(agent); err != nil {
				slog.Warn("could not wipe an agent's run secrets", "agent", agent, "error", err)
			}
			if err := egressPl.stop(agent); err != nil {
				slog.Warn("could not close an agent's egress", "agent", agent, "error", err)
			}
		}))
	term := tmux.New(tmux.WithSocket(cfg.Orchestrator.TmuxSocket))
	plane := control.New(term, ctlOpts...)

	hook := &launchHook{plane: plane}
	router := route.New(reg, hook, plane)
	layer := comm.New(store, router)
	// The layer routes by the defaults that belong to their channel's owner.
	defaultsCtl := newDefaultsAPI(defaults, cfg.Orchestrator.Channels, router.Resolve)
	layer.SetDefaults(defaultsCtl)
	layer.SetOperatorAccess(func(p core.Profile) bool {
		return access.Allowed(auth, access.Operator, access.Route, access.AgentResource(p.Name, p.Tags))
	})
	hook.layer = layer
	hook.follow = &followers{
		ctx:   ctx,
		layer: layer,
		reg:   reg,
		level: cfg.Orchestrator.Level,
		poll:  opts.MirrorPoll,
		seen:  make(map[string]context.CancelFunc),
		views: newViews(),
	}
	// The node's profiles, read and written from the management UI (ADR-0047
	// point 2); a profile saved while its agent runs is applied when it stops.
	var profiles *profileFiles
	if cfg.Orchestrator.ProfilesDir != "" {
		profiles = newProfileFiles(cfg.Orchestrator.ProfilesDir, cfg.Orchestrator, reg, checkProfile)
	}
	life := lifecycle{router: router, plane: plane, reg: reg, follow: hook.follow, profiles: profiles}
	saved, err := loadSavedStreams(filepath.Join(cfg.Orchestrator.StateDir, "streams.json"))
	if err != nil {
		return err
	}
	streams := &streamControl{
		layer: layer, reg: reg, views: hook.follow.views, level: cfg.Orchestrator.Level, saved: saved,
		send: func(ctx context.Context, agent string, m core.Message) error {
			_, err := layer.Send(ctx, agent, m)
			return err
		},
	}

	policy, err := gate.LoadPolicy(filepath.Join(cfg.Orchestrator.StateDir, "policy.json"), mcpsrv.GateRules(mcpsrv.DefaultServerName, upstream.Offered(cfg.Orchestrator.MCPUpstreams)...)...)
	if err != nil {
		return err
	}
	pending := gate.NewPending(gateNotifier{layer: layer})
	decider := &gateDecider{pending: pending}
	autoAllow := make(map[gate.Risk]bool)
	for _, r := range cfg.Orchestrator.Gate.AutoAllow {
		autoAllow[gate.Risk(r)] = true
	}
	gt := &gate.Gate{Policy: policy, Asker: pending, AutoAllow: autoAllow, AskTimeout: cfg.Orchestrator.Gate.AskTimeout}
	gateLn, err := gate.Listen(gateSock)
	if err != nil {
		return err
	}
	gateSrv := &http.Server{Handler: (&gate.Server{Gate: gt}).Handler(), ReadHeaderTimeout: 10 * time.Second}

	harvests := harvest.New(filepath.Join(cfg.Orchestrator.StateDir, "harvest"), auth)
	// A temporary agent's profile comes from a recipe, which may name an account
	// this node does not have; it is refused at spawn as a profile file naming one
	// is refused at load.
	check := func(p core.Profile) error {
		if err := checkProfile(p); err != nil {
			return err
		}
		return cfg.Orchestrator.CheckAccount(p.Account)
	}
	mgr := &temp.Manager{
		Registry: reg, Plane: plane, Starter: router, Channels: bindings{layer, hook.follow.views, saved},
		Workdirs: &workdir.Provisioner{Root: filepath.Join(cfg.Orchestrator.StateDir, "temp")},
		Harvests: harvests, Auth: auth,
		Recipes: func() (*recipe.Set, error) { return recipe.LoadDir(cfg.Orchestrator.RecipesDir) },
		Locate:  locate, Check: check, Gate: gt, Follow: hook.follow,
		Surface:    surface(layer, router),
		Thresholds: registry.Thresholds{UnusedAfter: cfg.Orchestrator.Temp.UnusedAfter, IdleAfter: cfg.Orchestrator.Temp.IdleAfter},
	}

	tempOps := &tempAPI{auth: auth, reg: reg, mgr: mgr, store: harvests}
	watch := newWatcher(reg, opts.MirrorPoll)
	watch.auth = auth
	streamOps := &streamAPI{auth: auth, streams: streams, watcher: watch}
	standing, err := loadBindingSet(filepath.Join(cfg.Orchestrator.StateDir, "bindings.json"), cfg, layer, subjectExists(ids), configured)
	if err != nil {
		return err
	}
	standingOps := &standingAPI{auth: auth, reg: reg, set: standing}
	lifeOps := &lifecycleAPI{auth: auth, life: life}
	ops := &mcpOps{reg: reg, thresholds: mgr.Thresholds, recipes: mgr.Recipes, mgr: mgr, store: harvests, layer: layer, auth: auth, resolve: router.Resolve, propose: cfg.Orchestrator.Governor.Propose}
	resolver := mcpsrv.Chain{
		mcpsrv.NewOperatorResolver(token),
		mcpsrv.AgentResolver{Tokens: tokens, Alive: func(agent string) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return plane.Alive(ctx, agent)
		}},
	}
	tasks := &task.Service{Store: &task.Store{Root: filepath.Join(cfg.Orchestrator.StateDir, "tasks")}, Auth: auth, Gate: gt,
		HasSecret: taskHasSecret(cfg.Orchestrator)}
	mcpServer := mcpsrv.New(ops, resolver, mcpsrv.WithAuthorizer(auth), mcpsrv.WithGate(gt), mcpsrv.WithProxy(proxy), mcpsrv.WithGovernor(cfg.Orchestrator.Governor.Agent),
		mcpsrv.WithChannels(governorChannels(cfg.Orchestrator.Providers, cfg.Orchestrator.Channels, governorServing)), mcpsrv.WithTasks(tasks))
	mcpServers.Store(mcpServer)
	mcpHandler := mcpServer.Handler()

	var meshNode *transport.Node
	var dir *directory.Directory
	var localOpts []local.Option
	if cfg.Mesh != nil {
		id, err := mesh.Load(cfg.Orchestrator.MeshKey, cfg.Orchestrator.Node)
		if err != nil {
			return fmt.Errorf("mesh: this node's key: %w (create it with `pao mesh keygen`)", err)
		}
		meshNode, err = transport.New(transport.Config{
			Self: id, Doc: cfg.Mesh, StateDir: cfg.Orchestrator.StateDir,
			Listen: cfg.Orchestrator.MeshListen, Listener: opts.MeshListener,
		})
		if err != nil {
			return fmt.Errorf("mesh: %w", err)
		}
		dir = directory.New(func(name string) bool {
			_, _, ok := reg.Lookup(name)
			return ok
		})
		meshNode.SetSink(dir)
		layer.SetNode(id.NodeID, meshNode.Knows)
		pending.SetNode(id.NodeID)
		meshNode.SetNodeServer(meshNodeServer{
			layer: layer, life: life, mgr: mgr, harvest: harvests, streams: streams,
			watcher: watch, pending: pending,
		})
		router.SetRemote(meshRemote{dir: dir, node: meshNode})
		ops.dir = dir
		tempOps.dir = dir
		streamOps.dir = dir
		standingOps.dir = dir
		lifeOps.dir = dir
		cmds := &meshCommands{self: id.NodeID, node: meshNode, dir: dir, holds: func(name string) bool {
			_, _, ok := reg.Lookup(name)
			return ok
		}}
		tempOps.cmds, streamOps.cmds, lifeOps.cmds, decider.cmds = cmds, cmds, cmds, cmds
		// A temporary agent may not take a name an agent on another node holds.
		mgr.Check = func(p core.Profile) error {
			if err := check(p); err != nil {
				return err
			}
			if dir.Has(p.Name) {
				return fmt.Errorf("the name %q is held by an agent on another node", p.Name)
			}
			return nil
		}
		localOpts = append(localOpts, local.WithMesh(meshStatusWith(meshNode, dir)))
	}

	layer.SetGovernor(cfg.Orchestrator.Governor.Agent, cfg.Orchestrator.Governor.Prefix)
	governorServes = servesAnOperator(layer.GovernorRequester, ids.IsOperator)
	if g := cfg.Orchestrator.Governor.Agent; g != "" {
		// A request from another subject than the last starts the governor on a
		// fresh context (ADR-0031 point 6): its session is stopped, and the
		// request's delivery starts it again.
		layer.SetGovernorReset(func(ctx context.Context) error { return life.Stop(ctx, g) })
	}
	layer.SetCommands(chatCommands{streams: streamOps, standing: standingOps, temps: tempOps, decider: decider, agents: ops, auth: auth, profiles: reg.Lookup})

	budgetCtx, stopBudget := context.WithCancel(ctx)
	defer stopBudget()
	book := newBudget(budgetCtx, cfg, layer, opts.BudgetReader)
	// Set before any surface serves, so no create can race the hook.
	fleet := taskFleet{reg: reg, router: router, governor: cfg.Orchestrator.Governor.Agent}
	dispatcher := task.NewDispatcher(tasks, fleet, book)
	warnOperators := func(text string) {
		if _, err := layer.NotifyOperators(budgetCtx, core.Message{Sender: "pao", Text: text}); err != nil {
			slog.Warn("could not warn the operator", "error", err)
		}
	}
	dispatcher.OneOffs = taskOneOffs{mgr: mgr, secrets: secrets, egress: egressPl, orch: cfg.Orchestrator, warn: warnOperators}
	dispatcher.SecretPath = secrets.Path
	dispatcher.Injected = func(secret string) (string, []string, bool) {
		r := injectRule(cfg.Orchestrator, secret)
		if r == nil {
			return "", nil, false
		}
		return r.Env, r.Hosts, true
	}
	dispatcher.Warn = warnOperators
	taskToolReach := newTaskTools(auth, cfg.Orchestrator.MCPUpstreams)
	dispatcher.Tools = taskToolReach
	tasks.Proxied = taskToolReach.Proxied
	dispatcher.Liveness = taskLiveness{reg: reg, thresholds: mgr.Thresholds}
	tasks.Changed, tasks.Tell = dispatcher.Kick, fleet.Tell
	localOpts = append(localOpts, local.WithBudget(budgetJSON(book)))
	if opts.ConfigPath != "" {
		startEnv := opts.StartEnv
		if startEnv == nil {
			startEnv = os.LookupEnv
		}
		adapters := opts.Adapters
		if adapters == nil {
			adapters = builtin.Table()
		}
		channelConfig := &channelConfigAPI{
			path: opts.ConfigPath, running: slices.Clone(cfg.Orchestrator.Channels), startEnv: startEnv, adapters: adapters,
		}
		localOpts = append(localOpts, local.WithChannelConfig(channelConfig), local.WithRestartCheck(channelConfig.RestartCheck))
	}
	uiSessions := webui.NewSessions()
	localOpts = append(localOpts, local.WithQuestions(questionsOf(pending)), local.WithRecipes(recipesOf(mgr)), local.WithChannels(channelsOf(cfg.Orchestrator.Providers, cfg.Orchestrator.Channels)))
	restartCh := make(chan struct{}, 1)
	localOpts = append(localOpts, local.WithRestart(func() {
		select {
		case restartCh <- struct{}{}:
		default: // a restart is already on its way
		}
	}))
	if profiles != nil {
		localOpts = append(localOpts, local.WithProfiles(profiles))
	}
	srv, err := local.New(layer, token, append([]local.Option{local.WithDecider(decider), local.WithTemp(tempOps), local.WithStream(streamOps), local.WithStanding(standingOps), local.WithWatch(streamOps), local.WithLifecycle(lifeOps), local.WithRejected(rejectedAPI{rejects}), local.WithTasks(taskAPI{tasks}), local.WithIdentity(identityAPI{s: ids, providers: providerNames(cfg.Orchestrator.Providers), owners: channelOwners(cfg.Orchestrator.Channels)}),
		local.WithDefaults(defaultsCtl), local.WithGrants(grantsAPI{store: grantStore, changed: mcpServer.Refresh}),
		local.WithMount("/mcp", mcpHandler),
		// The management UI (ADR-0047), on this same listener: its session is the
		// operator's, opened with the operator token.
		local.WithSessions(uiSessions), local.WithMount(webui.Prefix, webui.Handler(uiSessions, token)),
		local.WithNode(nodeInfo(cfg.Orchestrator.Node, reg))}, localOpts...)...)
	if err != nil {
		return err
	}
	layer.AddChannel(srv)

	table := opts.Adapters
	if table == nil {
		table = builtin.Table()
	}
	// Agents still running from before this start are taken back before any
	// channel can route to them (ADR-0051 point 2), and the tokens of agents not
	// taken back are let go.
	found, err := adopter{
		sessions: term.Sessions,
		tokens:   tokens,
		files:    tokenFiles,
		follow:   hook.follow,
		running:  func(agent string) error { return reg.SetState(agent, core.Running) },
	}.adopt(ctx, cfg.Profiles)
	if err != nil {
		return fmt.Errorf("take back running agents: %w", err)
	}
	found.log()
	if err := tokenFiles.prune(found.keep()); err != nil {
		return fmt.Errorf("agent tokens: %w", err)
	}
	adapters, err := wireChannels(cfg, creds, table, layer, ids, rejects)
	if err != nil {
		return err
	}
	// Every channel is known now: the bindings kept from before can be applied.
	streams.restore()
	adaptersCtx, stopAdapters := context.WithCancel(ctx)
	defer stopAdapters()
	var adaptersWG sync.WaitGroup
	for _, a := range adapters {
		adaptersWG.Add(1)
		go func() {
			defer adaptersWG.Done()
			if err := a.Run(adaptersCtx); err != nil && adaptersCtx.Err() == nil {
				slog.Error("provider stopped", "error", err)
			}
		}()
	}
	if text := found.notice(cfg.Orchestrator.TmuxSocket); text != "" {
		if _, err := layer.NotifyOperators(ctx, core.Message{Sender: "pao", Kind: core.MessageSystemEvent, Text: text}); err != nil {
			slog.Warn("could not tell the operator what was found running", "error", err)
		}
	}

	ln := opts.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Orchestrator.Listen); err != nil {
			return err
		}
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + ln.Addr().String() + "/mcp"
	mcpURL.Store(&url)
	egressPl.setListen(ln.Addr().String())
	mcpLn, err := gate.Listen(mcpSock)
	if err != nil {
		return fmt.Errorf("mcp socket: %w", err)
	}
	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp", mcpHandler)
	mcpSrv := &http.Server{Handler: mcpMux, ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 4)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	go func() { serveErr <- mcpSrv.Serve(mcpLn) }()
	go func() { serveErr <- gateSrv.Serve(gateLn) }()
	meshCtx, stopMesh := context.WithCancel(ctx)
	defer stopMesh()
	meshDone := make(chan struct{})
	if meshNode != nil {
		var meshWG sync.WaitGroup
		meshWG.Add(2)
		go func() {
			defer meshWG.Done()
			if err := meshNode.Serve(meshCtx); err != nil {
				serveErr <- fmt.Errorf("mesh: %w", err)
			}
		}()
		go func() {
			defer meshWG.Done()
			meshNode.RunReporter(meshCtx, registrySource{reg: reg, thresholds: mgr.Thresholds, layer: layer, views: hook.follow.views}, time.Second)
		}()
		go func() { meshWG.Wait(); close(meshDone) }()
	} else {
		close(meshDone)
	}
	go book.Run(budgetCtx, cfg.Orchestrator.Budget.Poll)
	go dispatcher.Run(budgetCtx, opts.DispatchEvery)
	slog.Info("pao serving", "node", cfg.Orchestrator.Node, "listen", ln.Addr().String(), "agents", len(cfg.Profiles))

	restarting := false
	select {
	case err := <-serveErr:
		_ = gateSrv.Close()
		_ = mcpSrv.Close()
		_ = httpSrv.Close()
		stopMesh()
		<-meshDone
		stopAdapters()
		adaptersWG.Wait()
		return err
	case <-ctx.Done():
	case <-restartCh:
		// Asked to restart from the management UI: stop as for a signal, and
		// let the caller start the node again. Agents keep running (ADR-0051).
		restarting = true
		slog.Info("pao restarting")
		stopRun()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Close rather than Shutdown for the gate: its requests wait on the
	// operator, and a question nobody will answer now must not hold the node up.
	_ = gateSrv.Close()
	_ = mcpSrv.Close()
	err = httpSrv.Shutdown(shutdown)
	if errors.Is(err, context.DeadlineExceeded) {
		// A client still holds a stream open; that is not a failure to stop.
		_ = httpSrv.Close()
		err = nil
	}
	stopMesh()
	<-meshDone
	stopAdapters()
	adaptersWG.Wait()
	hook.follow.wait()
	if restarting && err == nil {
		return ErrRestart
	}
	return err
}

// ErrRestart is what Run returns when it stopped because the operator asked
// pao to restart: the caller starts it again.
var ErrRestart = errors.New("pao was asked to restart")

// launchHook is the Launcher the router uses: it launches through the control
// plane, starts following the agent's transcript once it is up, and tells the
// agent's stderr destinations when it does not become ready.
type launchHook struct {
	plane  *control.Plane
	layer  *comm.Layer
	follow *followers
}

func (h *launchHook) Launch(ctx context.Context, p core.Profile) error {
	if err := checkProfile(p); err != nil {
		return fmt.Errorf("agent %q: %w", p.Name, err)
	}
	// Follow before launching: the transcript a fresh agent writes then arrives
	// whole (a file that appears after following began is read from its start),
	// instead of losing whatever the agent wrote before the follower caught up.
	h.follow.start(p)
	err := h.plane.Launch(ctx, p)
	if err == nil {
		return nil
	}
	if re, ok := control.AsReadinessError(err); ok {
		text := fmt.Sprintf("agent %q is not ready after %s. Its terminal pane:\n%s", p.Name, re.Timeout, re.Pane())
		if _, nerr := h.layer.NotifyAgent(ctx, p.Name, core.Message{Sender: "pao", Text: text}); nerr != nil {
			slog.Error("could not show the operator the pane", "agent", p.Name, "error", nerr)
		}
	}
	return err
}

// surface reports what a kill harvested to whoever asked for it: to each
// operator privately, never to a group, or to an agent as a message from pao.
func surface(layer *comm.Layer, router *route.Router) temp.Surface {
	return func(ctx context.Context, who access.Subject, text string) {
		switch who.Kind {
		case access.KindOperator:
			if _, err := layer.NotifyOperators(ctx, core.Message{Sender: "pao", Text: text}); err != nil {
				slog.Warn("could not report a harvest to the operator", "error", err)
			}
		case access.KindAgent:
			if _, err := router.Route(ctx, core.Target{Kind: core.ByName, Value: who.Name}, core.Message{Sender: "pao", Kind: core.MessageSystemEvent, Text: text}); err != nil {
				slog.Warn("could not report a harvest to the requesting agent", "agent", who.Name, "error", err)
			}
		}
	}
}

// followers runs one mirror per agent, started the first time the agent is
// up, and sends what the agent says to the human.
type followers struct {
	ctx   context.Context
	layer *comm.Layer
	reg   *registry.Registry
	level int
	poll  time.Duration
	// views holds how each agent's output is shown: its level, and a pause.
	views *views

	mu   sync.Mutex
	seen map[string]context.CancelFunc
	wg   sync.WaitGroup
}

// Stop ends the follower of the named agent, so a name that is spawned again
// later is followed afresh. Stopping one that is not followed does nothing.
func (f *followers) Stop(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cancel, ok := f.seen[name]; ok {
		cancel()
		delete(f.seen, name)
	}
}

func (f *followers) start(p core.Profile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.seen[p.Name]; ok {
		return
	}
	ctx, cancel := context.WithCancel(f.ctx)
	f.seen[p.Name] = cancel

	m := &mirror.Mirror{
		Reader:  readers[p.Transcript.Format],
		Poll:    f.poll,
		OnError: func(err error) { slog.Warn("transcript line skipped", "agent", p.Name, "error", err) },
	}
	levels := p.EffectiveLevels()
	path := expandPath(p.Transcript.Path)

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		err := m.Follow(ctx, path, mirror.FromEnd, func(e core.Entry) {
			// Any line the agent writes is activity, shown to the human or not.
			f.reg.Touch(p.Name, time.Now(), false)
			at := func(level int) (core.Message, bool) { return entryMessage(p, e, levels, level) }
			hold := func(m core.Message) bool { return f.views.hold(p.Name, m) }
			if err := f.layer.SendEntry(ctx, p.Name, f.views.level(p.Name, f.level), at, hold); err != nil {
				slog.Warn("could not send agent output", "agent", p.Name, "error", err)
			}
		})
		if err != nil {
			slog.Error("transcript follower stopped", "agent", p.Name, "error", err)
		}
	}()
}

func (f *followers) wait() { f.wg.Wait() }

// expandPath resolves a leading ~/ and environment variables in a profile path.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// hookMargin is how much longer than the gate's ask timeout a backend waits for
// the gate hook, so the gate's own denial arrives before the backend gives up.
const hookMargin = time.Minute

// backendLaunch is what every backend adapter is given to start an agent: this
// program as the gate hook, waited for longer than the gate asks, and pao's MCP
// server under the name the gate's rules for it use.
func backendLaunch(askTimeout time.Duration) (backend.Launch, error) {
	exe, err := os.Executable()
	if err != nil {
		return backend.Launch{}, fmt.Errorf("finding this program for the gate hook: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return backend.Launch{}, fmt.Errorf("finding the home directory: %w", err)
	}
	return backend.Launch{
		MCPServer:   mcpsrv.DefaultServerName,
		Hook:        "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' hook",
		HookTimeout: askTimeout + hookMargin,
		Home:        home,
	}, nil
}
