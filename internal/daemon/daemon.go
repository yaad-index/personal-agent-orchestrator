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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/agenttoken"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/tmux"
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
	// Authorizer is the access seam every surface asks. Nil means operator-only:
	// the operator may do anything and every agent nothing, until the access
	// matrix exists to say otherwise.
	Authorizer access.Authorizer
}

// Run serves the node until ctx is done. token is the local channel's bearer
// token.
func Run(ctx context.Context, cfg *config.Config, token string, opts Options) error {
	// Agents run under a tmux server this process starts, and a session inherits
	// the server's environment. The operator's token must not be in it: an agent
	// could otherwise read it from its own environment and speak as the operator.
	// This keeps it out of what agents inherit; it does not make it unreadable
	// to another process of the same user.
	if name := cfg.Orchestrator.TokenEnv; name != "" {
		_ = os.Unsetenv(name)
	}
	for _, p := range cfg.Profiles {
		if _, ok := readers[p.Transcript.Format]; !ok {
			return fmt.Errorf("agent %q: unknown transcript format %q", p.Name, p.Transcript.Format)
		}
	}

	reg := registry.New()
	for _, p := range cfg.Profiles {
		if err := reg.Add(p); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(cfg.Orchestrator.StateDir, 0o700); err != nil {
		return err
	}
	store, err := msgstore.Open(filepath.Join(cfg.Orchestrator.StateDir, "messages.db"), cfg.Orchestrator.MessageWindow)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	var ctlOpts []control.Option
	if opts.PollInterval > 0 {
		ctlOpts = append(ctlOpts, control.WithPollInterval(opts.PollInterval))
	}
	gateSock := filepath.Join(cfg.Orchestrator.StateDir, "gate.sock")
	// An agent calls pao as itself with a token minted when its session starts
	// and revoked when the session ends; the MCP address is filled in once the
	// node is listening.
	tokens := agenttoken.New()
	var mcpURL atomic.Pointer[string]
	ctlOpts = append(ctlOpts,
		control.WithEnv(func(agent string) []string {
			env := []string{"PAO_AGENT=" + agent, "PAO_GATE_SOCKET=" + gateSock, "PAO_AGENT_TOKEN=" + tokens.Mint(agent)}
			if u := mcpURL.Load(); u != nil {
				env = append(env, "PAO_MCP_URL="+*u)
			}
			return env
		}),
		control.WithOnStop(tokens.Revoke))
	plane := control.New(tmux.New(tmux.WithSocket(cfg.Orchestrator.TmuxSocket)), ctlOpts...)

	hook := &launchHook{plane: plane}
	router := route.New(reg, hook, plane)
	layer := comm.New(store, router)
	hook.layer = layer
	hook.follow = &followers{
		ctx:   ctx,
		layer: layer,
		reg:   reg,
		level: cfg.Orchestrator.Level,
		poll:  opts.MirrorPoll,
		seen:  make(map[string]context.CancelFunc),
	}

	policy, err := gate.LoadPolicy(filepath.Join(cfg.Orchestrator.StateDir, "policy.json"))
	if err != nil {
		return err
	}
	pending := gate.NewPending(func(ctx context.Context, text string) error {
		_, err := layer.Notify(ctx, core.Message{Sender: "pao", Text: text})
		return err
	})
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

	harvests := harvest.New(filepath.Join(cfg.Orchestrator.StateDir, "harvest"), opts.Authorizer)
	mgr := &temp.Manager{
		Registry: reg, Plane: plane, Starter: router, Channels: layer,
		Workdirs: &workdir.Provisioner{Root: filepath.Join(cfg.Orchestrator.StateDir, "temp")},
		Harvests: harvests, Auth: opts.Authorizer,
		Recipes: func() (*recipe.Set, error) { return recipe.LoadDir(cfg.Orchestrator.RecipesDir) },
		Locate:  locate, Check: checkProfile, Gate: gt, Follow: hook.follow,
		Surface:    surface(layer, router),
		Thresholds: registry.Thresholds{UnusedAfter: cfg.Orchestrator.Temp.UnusedAfter, IdleAfter: cfg.Orchestrator.Temp.IdleAfter},
	}

	ops := mcpOps{reg: reg, thresholds: mgr.Thresholds, recipes: mgr.Recipes, mgr: mgr, store: harvests, layer: layer, auth: opts.Authorizer}
	resolver := mcpsrv.Chain{
		mcpsrv.NewOperatorResolver(token),
		mcpsrv.AgentResolver{Tokens: tokens, Alive: func(agent string) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return plane.Alive(ctx, agent)
		}},
	}
	mcpHandler := mcpsrv.New(ops, resolver, mcpsrv.WithAuthorizer(opts.Authorizer)).Handler()

	srv, err := local.New(layer, token, local.WithDecider(pending), local.WithTemp(tempAPI{mgr: mgr, store: harvests}),
		local.WithMount("/mcp", mcpHandler))
	if err != nil {
		return err
	}
	layer.AddChannel(srv)

	ln := opts.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Orchestrator.Listen); err != nil {
			return err
		}
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + ln.Addr().String() + "/mcp"
	mcpURL.Store(&url)

	serveErr := make(chan error, 2)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	go func() { serveErr <- gateSrv.Serve(gateLn) }()
	slog.Info("pao serving", "node", cfg.Orchestrator.Node, "listen", ln.Addr().String(), "agents", len(cfg.Profiles))

	select {
	case err := <-serveErr:
		_ = gateSrv.Close()
		_ = httpSrv.Close()
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Close rather than Shutdown for the gate: its requests wait on the
	// operator, and a question nobody will answer now must not hold the node up.
	_ = gateSrv.Close()
	err = httpSrv.Shutdown(shutdown)
	if errors.Is(err, context.DeadlineExceeded) {
		// A client still holds a stream open; that is not a failure to stop.
		_ = httpSrv.Close()
		err = nil
	}
	hook.follow.wait()
	return err
}

// launchHook is the Launcher the router uses: it launches through the control
// plane, starts following the agent's transcript once it is up, and tells the
// operator, and only the operator, when an agent does not become ready.
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
		if _, nerr := h.layer.Notify(ctx, core.Message{Sender: "pao", Text: text}); nerr != nil {
			slog.Error("could not show the operator the pane", "agent", p.Name, "error", nerr)
		}
	}
	return err
}

// surface reports what a kill harvested to whoever asked for it: to the
// operator as an operator-only message, to an agent as a message from pao.
func surface(layer *comm.Layer, router *route.Router) temp.Surface {
	return func(ctx context.Context, who access.Subject, text string) {
		switch who.Kind {
		case access.KindOperator:
			if _, err := layer.Notify(ctx, core.Message{Sender: "pao", Text: text}); err != nil {
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
			text, ok := render(e, levels, f.level)
			if !ok {
				return
			}
			if _, err := f.layer.Send(ctx, p.Name, core.Message{Sender: p.Name, Kind: core.MessageContent, Text: text, Time: e.Time}); err != nil {
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
