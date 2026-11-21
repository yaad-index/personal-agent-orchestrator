package mcpsrv

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

// Tasks is the task operations (ADR-0036). They decide, under the access matrix
// and the gate, what who may create and see: this package never decides for them.
type Tasks interface {
	Create(ctx context.Context, who access.Subject, n task.New) (task.Task, error)
	List(ctx context.Context, who access.Subject) ([]task.Entry, error)
	Get(ctx context.Context, who access.Subject, id string) (task.Entry, error)
}

// WithTasks offers the task tools to every subject but the governor, which
// translates commands and runs no work.
func WithTasks(t Tasks) Option { return func(s *Server) { s.tasks = t } }

// The task tools.
const (
	ToolCreateTask = "create_task"
	ToolListTasks  = "list_tasks"
	ToolGetTask    = "get_task"
)

// TaskTools are the names of the task tools.
var TaskTools = []string{ToolCreateTask, ToolListTasks, ToolGetTask}

// TaskInfo is a task as a tool reports it.
type TaskInfo struct {
	task.Task
	Folder string `json:"folder" jsonschema:"the folder it is kept in: open, archive, or another"`
}

type createTaskInput struct {
	Title       string   `json:"title" jsonschema:"a short one-line name for the task"`
	Description string   `json:"description" jsonschema:"what to do"`
	Done        string   `json:"done" jsonschema:"the definition of done: what the agent working it reports as met"`
	How         string   `json:"how,omitempty" jsonschema:"the skill, tool or template it is done with"`
	Tags        []string `json:"tags" jsonschema:"the tags of the agents that may run it; each must be a tag your own access reaches"`
	Parent      string   `json:"parent,omitempty" jsonschema:"the id of the task this one is created from, such as the task a review reviews"`
	Tools       []string `json:"tools,omitempty" jsonschema:"tools the task uses; they are still bound by the running agent's own access and the gate"`
	Scripts     []string `json:"scripts,omitempty" jsonschema:"scripts the task runs"`
	Secret      string   `json:"secret,omitempty" jsonschema:"the NAME of a secret the run needs, never its value"`
}

type taskOutput struct {
	Task TaskInfo `json:"task"`
}

type listTasksOutput struct {
	Tasks []TaskInfo `json:"tasks"`
}

type getTaskInput struct {
	ID string `json:"id" jsonschema:"the task's id"`
}

func (s *Server) addTaskTools(srv *mcp.Server) {
	notDestructive := false
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolCreateTask,
		Description: "Create a task: a unit of work pao dispatches to an agent carrying its tags. It is created open and owned by you. Each tag must be one your own access reaches. Attach a secret by its name only, never its value. Creating is gated: you may be asked to wait for the operator.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.createTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolListTasks,
		Description: "List the tasks you may see, newest first: those you own, and those whose tags your access reaches.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listTasks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolGetTask,
		Description: "Read one task by its id.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getTask)
}

func (s *Server) createTask(ctx context.Context, req *mcp.CallToolRequest, in createTaskInput) (*mcp.CallToolResult, taskOutput, error) {
	t, err := s.tasks.Create(ctx, subject(req), task.New{
		Title: in.Title, Description: in.Description, Done: in.Done, How: in.How, Tags: in.Tags, Parent: in.Parent,
		Attachments: task.Attachments{Tools: in.Tools, Scripts: in.Scripts, Secret: in.Secret},
	})
	if err != nil {
		return nil, taskOutput{}, taskError(err)
	}
	return nil, taskOutput{Task: TaskInfo{Task: t, Folder: task.FolderOpen}}, nil
}

func (s *Server) listTasks(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listTasksOutput, error) {
	es, err := s.tasks.List(ctx, subject(req))
	out := listTasksOutput{Tasks: []TaskInfo{}}
	for _, e := range es {
		out.Tasks = append(out.Tasks, TaskInfo{Task: e.Task, Folder: e.Folder})
	}
	return nil, out, taskError(err)
}

func (s *Server) getTask(ctx context.Context, req *mcp.CallToolRequest, in getTaskInput) (*mcp.CallToolResult, taskOutput, error) {
	if in.ID == "" {
		return nil, taskOutput{}, errors.New("an id is required")
	}
	e, err := s.tasks.Get(ctx, subject(req), in.ID)
	if err != nil {
		return nil, taskOutput{}, taskError(err)
	}
	return nil, taskOutput{Task: TaskInfo{Task: e.Task, Folder: e.Folder}}, nil
}

// taskError keeps a gate denial recognisable as this package's own.
func taskError(err error) error {
	if errors.Is(err, task.ErrDenied) {
		return errors.Join(ErrDenied, err)
	}
	return err
}
