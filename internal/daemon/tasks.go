package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/runsecret"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/upstream"
)

// taskAPI is the task service as the local channel serves it. The channel's
// bearer token identifies the operator, so every call is made as the operator.
type taskAPI struct{ svc *task.Service }

var _ local.TasksAPI = taskAPI{}

func (a taskAPI) Create(ctx context.Context, n local.NewTaskJSON) (local.TaskJSON, error) {
	t, err := a.svc.Create(ctx, access.Operator, task.New{Title: n.Title, Description: n.Description, Done: n.Done, How: n.How, Tags: n.Tags, Parent: n.Parent, Attachments: n.Attachments})
	if err != nil {
		return local.TaskJSON{}, err
	}
	return local.TaskJSON{Task: t, Folder: task.FolderOpen}, nil
}

func (a taskAPI) List(ctx context.Context) ([]local.TaskJSON, error) {
	es, err := a.svc.List(ctx, access.Operator)
	var out []local.TaskJSON
	for _, e := range es {
		out = append(out, local.TaskJSON{Task: e.Task, Folder: e.Folder})
	}
	return out, err
}

func (a taskAPI) Get(ctx context.Context, id string) (local.TaskJSON, error) {
	e, err := a.svc.Get(ctx, access.Operator, id)
	if err != nil {
		return local.TaskJSON{}, err
	}
	return local.TaskJSON{Task: e.Task, Folder: e.Folder}, nil
}

func (a taskAPI) Cancel(ctx context.Context, id, reason string) (local.TaskJSON, error) {
	t, err := a.svc.Cancel(ctx, access.Operator, id, reason)
	if err != nil {
		return local.TaskJSON{}, err
	}
	return local.TaskJSON{Task: t, Folder: task.FolderArchive}, nil
}

// taskFleet is the node's agents as dispatch sees them: this node's agents, but
// never the governor, which translates commands and runs no work.
type taskFleet struct {
	reg      *registry.Registry
	router   *route.Router
	governor string
}

func (f taskFleet) Candidates() []task.Candidate {
	var out []task.Candidate
	for _, a := range f.reg.All() {
		p := a.Profile
		if p.Node != "" || (f.governor != "" && p.Name == f.governor) {
			continue
		}
		out = append(out, task.Candidate{Name: p.Name, Tags: p.Tags, Account: p.Account, Budget: p.Budget, Running: a.State == core.Running})
	}
	return out
}

func (f taskFleet) Deliver(ctx context.Context, agent, text string) error {
	p, _, ok := f.reg.Lookup(agent)
	if !ok {
		return fmt.Errorf("agent %q is not registered", agent)
	}
	for _, r := range f.router.Deliver(ctx, []core.Profile{p}, core.Message{Sender: task.Pao, Text: text}) {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// taskOneOffs spawns and kills the temporary agents pao starts for tasks,
// through the temporary-agent manager, with the task's owner as requester: the
// access matrix and the gate judge each as the owner's own spawn or kill.
type taskOneOffs struct {
	mgr     *temp.Manager
	secrets *runsecret.Store
}

func (o taskOneOffs) Recipes() []task.Recipe {
	set, err := o.mgr.Recipes()
	if err != nil {
		slog.Warn("dispatch: recipes could not be read", "error", err)
		return nil
	}
	var out []task.Recipe
	for _, n := range set.Names() {
		r, _ := set.Get(n)
		out = append(out, task.Recipe{Name: n, Tags: r.Profile.Tags, Account: r.Profile.Account, Budget: r.Profile.Budget})
	}
	return out
}

func (o taskOneOffs) Spawn(ctx context.Context, owner access.Subject, recipe, secret string) (string, error) {
	req := temp.SpawnRequest{Recipe: recipe, Requester: owner, Channel: "local"}
	if secret != "" {
		// The agent's name is chosen first, so its secret is in place before
		// it starts: its environment is made once, at launch.
		name, err := oneOffName(recipe)
		if err != nil {
			return "", err
		}
		if _, err := o.secrets.Materialize(name, secret); err != nil {
			return "", err
		}
		req.Name = name
	}
	// A task's one-off speaks on the local channel, the operator's own, whatever
	// other channels the node has.
	p, err := o.mgr.Spawn(ctx, req)
	if err != nil {
		if p.Name != "" {
			// Registered but not ready: it cannot take the task, so it goes.
			_, _ = o.mgr.Kill(ctx, temp.KillRequest{Name: p.Name, Requester: owner, NoReport: true})
		}
		if req.Name != "" {
			if werr := o.secrets.Wipe(req.Name); werr != nil {
				slog.Warn("dispatch: could not wipe the secret of a one-off that did not start", "agent", req.Name, "error", werr)
			}
		}
		return "", err
	}
	return p.Name, nil
}

// oneOffName is a fresh name for an agent from recipe, as the manager would
// choose.
func oneOffName(recipe string) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return recipe + "-" + hex.EncodeToString(b[:]), nil
}

func (o taskOneOffs) Kill(ctx context.Context, owner access.Subject, agent string) (string, bool, error) {
	// The harvest goes in the task's log, so no one else is told of it.
	res, err := o.mgr.Kill(ctx, temp.KillRequest{Name: agent, Requester: owner, NoReport: true})
	if errors.Is(err, temp.ErrNotFound) {
		if _, ok := o.mgr.Registry.TempOf(agent); !ok {
			return "", true, nil
		}
	}
	if err != nil {
		return "", false, err
	}
	return res.Harvest.ID, false, nil
}

// taskLiveness says a temporary assignee has gone quiet when its status flag
// is idle. A configured agent carries no such flag yet, so it is never called
// quiet.
type taskLiveness struct {
	reg        *registry.Registry
	thresholds registry.Thresholds
}

func (l taskLiveness) Stalled(agent string) bool {
	v, ok := l.reg.TempOf(agent)
	return ok && v.Flag(time.Now(), l.thresholds) == registry.FlagIdle
}

// Tell tells a running agent; one that is not running has no work to stop, and
// telling it would only start it.
func (f taskFleet) Tell(ctx context.Context, agent, text string) error {
	if _, state, ok := f.reg.Lookup(agent); !ok || state != core.Running {
		return nil
	}
	return f.Deliver(ctx, agent, text)
}

// resolveSecret reads a secret's value from where the orchestrator config holds
// it: a variable's value as read at start, before it was taken out of the
// environment, or a file's as it is now. Its errors name the secret and where
// it was looked for, never a value.
func resolveSecret(o config.Orchestrator, fromEnv map[string]string) func(name string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		s, ok := o.Secret(name)
		switch {
		case !ok:
			return nil, fmt.Errorf("no secret named %q is configured", name)
		case s.Env != "":
			v := fromEnv[name]
			if v == "" {
				return nil, fmt.Errorf("secret %q: variable %s is empty", name, s.Env)
			}
			return []byte(v), nil
		default:
			b, err := os.ReadFile(s.File)
			if err != nil {
				return nil, fmt.Errorf("secret %q: %w", name, err)
			}
			return b, nil
		}
	}
}

// taskTools says which tool names are pao's proxied tools, and whether an
// agent's own access reaches one.
type taskTools struct {
	auth  access.Authorizer
	tools map[string][]string
}

func newTaskTools(auth access.Authorizer, ups []config.MCPUpstream) taskTools {
	t := taskTools{auth: auth, tools: map[string][]string{}}
	for _, u := range upstream.Offered(ups) {
		t.tools[u.Name] = u.Tags
	}
	return t
}

func (t taskTools) Proxied(tool string) bool { _, ok := t.tools[tool]; return ok }

func (t taskTools) Reaches(agent, tool string) bool {
	tags, ok := t.tools[tool]
	return ok && access.Allowed(t.auth, access.Agent(agent), access.CallTool, access.ToolResource(tool, tags))
}
