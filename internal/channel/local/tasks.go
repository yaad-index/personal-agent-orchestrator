package local

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

// TaskJSON is a task and the folder it is kept in.
type TaskJSON struct {
	task.Task
	Folder string `json:"folder"`
}

// NewTaskJSON is a request to create a task.
type NewTaskJSON struct {
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Done        string           `json:"done"`
	How         string           `json:"how,omitempty"`
	Tags        []string         `json:"tags"`
	Parent      string           `json:"parent,omitempty"`
	Attachments task.Attachments `json:"attachments,omitzero"`
}

// TasksAPI is the operator's task operations, under the same checks as every
// other surface's.
type TasksAPI interface {
	Create(ctx context.Context, n NewTaskJSON) (TaskJSON, error)
	List(ctx context.Context) ([]TaskJSON, error)
	Get(ctx context.Context, id string) (TaskJSON, error)
	Cancel(ctx context.Context, id, reason string) (TaskJSON, error)
}

// WithTasks serves the task routes.
func WithTasks(a TasksAPI) Option { return func(s *Server) { s.tasks = a } }

func (s *Server) registerTasks(mux *http.ServeMux) {
	if s.tasks == nil {
		return
	}
	mux.HandleFunc("POST /v1/tasks", s.auth(s.createTask))
	mux.HandleFunc("GET /v1/tasks", s.auth(s.listTasks))
	mux.HandleFunc("GET /v1/tasks/{id}", s.auth(s.getTask))
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.auth(s.cancelTask))
}

// CancelTaskJSON is a request to cancel a task.
type CancelTaskJSON struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	var req CancelTaskJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "a cancel request is a JSON object")
		return
	}
	t, err := s.tasks.Cancel(r.Context(), r.PathValue("id"), req.Reason)
	if err != nil {
		writeError(w, taskStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func taskStatus(err error) int {
	switch {
	case errors.Is(err, task.ErrNotFound), errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, task.ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, task.ErrDenied):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req NewTaskJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "a task request is a JSON object")
		return
	}
	t, err := s.tasks.Create(r.Context(), req)
	if err != nil {
		writeError(w, taskStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.List(r.Context())
	if err != nil && len(out) == 0 {
		writeError(w, taskStatus(err), err.Error())
		return
	}
	if out == nil {
		out = []TaskJSON{}
	}
	// Tasks that read are listed even when some file did not: the operator is
	// told of those in the header, to mend them.
	if err != nil {
		w.Header().Set("Pao-Warning", url.QueryEscape(err.Error()))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, taskStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// CreateTask creates a task as the operator.
func (c *Client) CreateTask(ctx context.Context, n NewTaskJSON) (TaskJSON, error) {
	var out TaskJSON
	err := c.do(ctx, http.MethodPost, "/v1/tasks", n, http.StatusCreated, &out)
	return out, err
}

// Tasks lists the tasks the operator may see.
func (c *Client) Tasks(ctx context.Context) ([]TaskJSON, error) {
	var out []TaskJSON
	err := c.do(ctx, http.MethodGet, "/v1/tasks", nil, http.StatusOK, &out)
	return out, err
}

// CancelTask cancels a task as the operator.
func (c *Client) CancelTask(ctx context.Context, id, reason string) (TaskJSON, error) {
	var out TaskJSON
	err := c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/cancel", CancelTaskJSON{Reason: reason}, http.StatusOK, &out)
	return out, err
}

// Task reads one task.
func (c *Client) Task(ctx context.Context, id string) (TaskJSON, error) {
	var out TaskJSON
	err := c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), nil, http.StatusOK, &out)
	return out, err
}
