package local

import (
	"context"
	"net/http"
	"net/url"
)

// LifecycleAPI is the operator's start, stop and restart of a configured agent: one
// of this node's or, at the master, one on another node. The server calls it on
// behalf of the operator, the only caller the bearer token identifies.
type LifecycleAPI interface {
	// Start brings the agent up if it is not running. On ErrNotReady the agent
	// exists and was not ready in time.
	Start(ctx context.Context, name string) error
	// Stop ends the agent's session; it starts again with its next message.
	Stop(ctx context.Context, name string) error
	// Restart stops the agent and starts it again.
	Restart(ctx context.Context, name string) error
}

// WithLifecycle serves the agent lifecycle routes.
func WithLifecycle(a LifecycleAPI) Option { return func(s *Server) { s.lifecycle = a } }

func (s *Server) registerLifecycle(mux *http.ServeMux) {
	if s.lifecycle == nil {
		return
	}
	mux.HandleFunc("POST /v1/agents/{name}/{action}", s.auth(s.agentAction))
}

func (s *Server) agentAction(w http.ResponseWriter, r *http.Request) {
	var do func(context.Context, string) error
	switch r.PathValue("action") {
	case "start":
		do = s.lifecycle.Start
	case "stop":
		do = s.lifecycle.Stop
	case "restart":
		do = s.lifecycle.Restart
	default:
		writeError(w, http.StatusNotFound, "an agent action is start, stop or restart")
		return
	}
	if err := do(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AgentAction runs "start", "stop" or "restart" on a configured agent.
func (c *Client) AgentAction(ctx context.Context, name, action string) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(name)+"/"+url.PathEscape(action), nil, http.StatusNoContent, nil)
}
