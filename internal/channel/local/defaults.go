package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// DefaultJSON is one channel's default agent (ADR-0023 point 5).
type DefaultJSON struct {
	Channel string `json:"channel,omitempty"`
	Agent   string `json:"agent"`
}

// DefaultsJSON is every channel's default agent, and the version of the stored
// document they come from.
type DefaultsJSON struct {
	Version  uint64        `json:"version"`
	Defaults []DefaultJSON `json:"defaults"`
}

// DefaultChangeJSON is what setting or clearing a default did: the agent the
// channel had before, and the channel the agent stopped being the default of.
type DefaultChangeJSON struct {
	Channel  string `json:"channel"`
	Agent    string `json:"agent,omitempty"`
	Previous string `json:"previous,omitempty"`
	Left     string `json:"left,omitempty"`
}

// DefaultsAPI is the operator's control of the channels' default agents, changed
// while the node runs.
type DefaultsAPI interface {
	Defaults(ctx context.Context) (DefaultsJSON, error)
	SetDefault(ctx context.Context, channel, agent string) (DefaultChangeJSON, error)
	ClearDefault(ctx context.Context, channel string) (DefaultChangeJSON, error)
}

// WithDefaults serves the default-agent routes.
func WithDefaults(a DefaultsAPI) Option { return func(s *Server) { s.defaults = a } }

func (s *Server) registerDefaults(mux *http.ServeMux) {
	if s.defaults == nil {
		return
	}
	mux.HandleFunc("GET /v1/channels/defaults", s.auth(s.listDefaults))
	mux.HandleFunc("PUT /v1/channels/{channel}/default", s.auth(s.setDefault))
	mux.HandleFunc("DELETE /v1/channels/{channel}/default", s.auth(s.clearDefault))
}

func (s *Server) listDefaults(w http.ResponseWriter, r *http.Request) {
	out, err := s.defaults.Defaults(r.Context())
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out.Defaults == nil {
		out.Defaults = []DefaultJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setDefault(w http.ResponseWriter, r *http.Request) {
	var req DefaultJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Agent == "" {
		writeError(w, http.StatusBadRequest, "a default request names an agent")
		return
	}
	out, err := s.defaults.SetDefault(r.Context(), r.PathValue("channel"), req.Agent)
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) clearDefault(w http.ResponseWriter, r *http.Request) {
	out, err := s.defaults.ClearDefault(r.Context(), r.PathValue("channel"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Defaults lists every channel's default agent.
func (c *Client) Defaults(ctx context.Context) (DefaultsJSON, error) {
	var out DefaultsJSON
	err := c.do(ctx, http.MethodGet, "/v1/channels/defaults", nil, http.StatusOK, &out)
	return out, err
}

// SetDefault makes agent the channel's default agent.
func (c *Client) SetDefault(ctx context.Context, channel, agent string) (DefaultChangeJSON, error) {
	var out DefaultChangeJSON
	err := c.do(ctx, http.MethodPut, "/v1/channels/"+url.PathEscape(channel)+"/default", DefaultJSON{Agent: agent}, http.StatusOK, &out)
	return out, err
}

// ClearDefault removes the channel's default agent.
func (c *Client) ClearDefault(ctx context.Context, channel string) (DefaultChangeJSON, error) {
	var out DefaultChangeJSON
	err := c.do(ctx, http.MethodDelete, "/v1/channels/"+url.PathEscape(channel)+"/default", nil, http.StatusOK, &out)
	return out, err
}
