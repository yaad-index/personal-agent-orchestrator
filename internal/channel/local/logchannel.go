package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// LogPauser pauses and resumes an agent's log channel (ADR-0058 point 7).
type LogPauser interface {
	PauseLog(agent string, pause bool) error
}

// LogJSON pauses an agent's log channel, or resumes it.
type LogJSON struct {
	Paused bool `json:"paused"`
}

// WithLogs serves the route that pauses and resumes the agents' log channels.
func WithLogs(p LogPauser) Option { return func(s *Server) { s.logs = p } }

func (s *Server) registerLogs(mux *http.ServeMux) {
	if s.logs == nil {
		return
	}
	mux.HandleFunc("PUT /v1/agents/{agent}/log", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var req LogJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "the body must say whether the log channel is paused")
			return
		}
		if err := s.logs.PauseLog(r.PathValue("agent"), req.Paused); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// PauseLog pauses an agent's log channel, or resumes it.
func (c *Client) PauseLog(ctx context.Context, agent string, pause bool) error {
	return c.do(ctx, http.MethodPut, "/v1/agents/"+url.PathEscape(agent)+"/log", LogJSON{Paused: pause}, http.StatusNoContent, nil)
}
