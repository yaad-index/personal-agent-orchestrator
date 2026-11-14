package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// StreamJSON binds an agent's output to a channel. An empty channel clears it.
type StreamJSON struct {
	Channel string `json:"channel"`
	// Level is the verbosity the output is rendered at; 0 keeps the node's own.
	Level int `json:"level,omitempty"`
}

// ViewJSON is a view control: pause, resume or backtrack.
type ViewJSON struct {
	Action string `json:"action"`
	// Level and Entries are for a backtrack: the level to show and how many entries.
	Level   int `json:"level,omitempty"`
	Entries int `json:"entries,omitempty"`
}

// StreamAPI is the operator's control of how an agent's output is shown, on this
// node's agents and, at the master, on agents of other nodes. The server calls it on
// behalf of the operator, the only caller the bearer token identifies.
type StreamAPI interface {
	SetStream(ctx context.Context, agent, channel string, level int) error
	// View's action is "pause", "resume" or "backtrack".
	View(ctx context.Context, agent, action string, level, entries int) error
}

// WithStream serves the stream and view routes.
func WithStream(a StreamAPI) Option { return func(s *Server) { s.streams = a } }

func (s *Server) registerStream(mux *http.ServeMux) {
	if s.streams == nil {
		return
	}
	mux.HandleFunc("PUT /v1/stream/{agent}", s.auth(s.setStream))
	mux.HandleFunc("POST /v1/view/{agent}", s.auth(s.view))
}

func (s *Server) setStream(w http.ResponseWriter, r *http.Request) {
	var req StreamJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "a stream request names a channel, or none to clear it")
		return
	}
	if err := s.streams.SetStream(r.Context(), r.PathValue("agent"), req.Channel, req.Level); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) view(w http.ResponseWriter, r *http.Request) {
	var req ViewJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "a view request names an action")
		return
	}
	switch req.Action {
	case "pause", "resume", "backtrack":
	default:
		writeError(w, http.StatusBadRequest, "a view action is pause, resume or backtrack")
		return
	}
	if err := s.streams.View(r.Context(), r.PathValue("agent"), req.Action, req.Level, req.Entries); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetStream binds an agent's output to a channel (empty clears it) at a level.
func (c *Client) SetStream(ctx context.Context, agent, channel string, level int) error {
	return c.do(ctx, http.MethodPut, "/v1/stream/"+url.PathEscape(agent), StreamJSON{Channel: channel, Level: level}, http.StatusNoContent, nil)
}

// View pauses, resumes or backtracks the operator's view of an agent's output.
func (c *Client) View(ctx context.Context, agent, action string, level, entries int) error {
	return c.do(ctx, http.MethodPost, "/v1/view/"+url.PathEscape(agent), ViewJSON{Action: action, Level: level, Entries: entries}, http.StatusNoContent, nil)
}
