package daemon

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
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
