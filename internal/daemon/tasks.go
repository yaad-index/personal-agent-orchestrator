package daemon

import (
	"context"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
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
