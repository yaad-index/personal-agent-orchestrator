package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
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
type taskOneOffs struct{ mgr *temp.Manager }

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

func (o taskOneOffs) Spawn(ctx context.Context, owner access.Subject, recipe string) (string, error) {
	// A task's one-off speaks on the local channel, the operator's own, whatever
	// other channels the node has.
	p, err := o.mgr.Spawn(ctx, temp.SpawnRequest{Recipe: recipe, Requester: owner, Channel: "local"})
	if err != nil {
		if p.Name != "" {
			// Registered but not ready: it cannot take the task, so it goes.
			_, _ = o.mgr.Kill(ctx, temp.KillRequest{Name: p.Name, Requester: owner})
		}
		return "", err
	}
	return p.Name, nil
}

func (o taskOneOffs) Kill(ctx context.Context, owner access.Subject, agent string) (string, bool, error) {
	res, err := o.mgr.Kill(ctx, temp.KillRequest{Name: agent, Requester: owner})
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
