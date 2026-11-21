package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// StandingJSON is one standing binding (ADR-0029 points 3 and 8): a destination,
// by name, following one of an agent's streams.
type StandingJSON struct {
	Agent string `json:"agent"`
	// Stream is "stdout" or "stderr"; empty is stdout.
	Stream string `json:"stream,omitempty"`
	// To is the destination: group:<name>, private:<subject>[@<channel>] or
	// channel:local.
	To string `json:"to"`
	// Level is a stdout binding's level, a number or "log"; empty is the node's.
	Level string `json:"level,omitempty"`
	// Prefix is shown in place of the agent's name, on a stdout binding.
	Prefix string `json:"prefix,omitempty"`
	// From says where the binding was set, "config" or "runtime". It is set in a
	// listing and ignored in a request.
	From string `json:"from,omitempty"`
}

// StandingListJSON is a listing of standing bindings.
type StandingListJSON struct {
	Bindings []StandingJSON `json:"bindings"`
}

// StandingAPI is the operator's control of the standing bindings while the node
// runs. Only an agent the operator may reach can be bound, unbound or listed.
type StandingAPI interface {
	// Bindings lists the standing bindings, of one agent when agent is not empty.
	Bindings(ctx context.Context, agent string) (StandingListJSON, error)
	// Bind sets a binding and returns it as it was set, defaults filled in.
	Bind(ctx context.Context, b StandingJSON) (StandingJSON, error)
	// Unbind removes a binding set while the node runs.
	Unbind(ctx context.Context, b StandingJSON) (StandingJSON, error)
}

// WithStanding serves the standing-binding routes.
func WithStanding(a StandingAPI) Option { return func(s *Server) { s.standing = a } }

func (s *Server) registerStanding(mux *http.ServeMux) {
	if s.standing == nil {
		return
	}
	mux.HandleFunc("GET /v1/bindings", s.auth(s.listStanding))
	mux.HandleFunc("POST /v1/bindings", s.auth(s.addStanding))
	mux.HandleFunc("DELETE /v1/bindings", s.auth(s.removeStanding))
}

func (s *Server) listStanding(w http.ResponseWriter, r *http.Request) {
	out, err := s.standing.Bindings(r.Context(), r.URL.Query().Get("agent"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out.Bindings == nil {
		out.Bindings = []StandingJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addStanding(w http.ResponseWriter, r *http.Request) {
	var b StandingJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "a binding names an agent and a destination")
		return
	}
	out, err := s.standing.Bind(r.Context(), b)
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) removeStanding(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.standing.Unbind(r.Context(), StandingJSON{Agent: q.Get("agent"), Stream: q.Get("stream"), To: q.Get("to")})
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// StandingBindings lists the standing bindings, of one agent when agent is not empty.
func (c *Client) StandingBindings(ctx context.Context, agent string) (StandingListJSON, error) {
	path := "/v1/bindings"
	if agent != "" {
		path += "?agent=" + url.QueryEscape(agent)
	}
	var out StandingListJSON
	err := c.do(ctx, http.MethodGet, path, nil, http.StatusOK, &out)
	return out, err
}

// BindStanding sets a standing binding.
func (c *Client) BindStanding(ctx context.Context, b StandingJSON) (StandingJSON, error) {
	var out StandingJSON
	err := c.do(ctx, http.MethodPost, "/v1/bindings", b, http.StatusOK, &out)
	return out, err
}

// UnbindStanding removes a standing binding set while the node runs.
func (c *Client) UnbindStanding(ctx context.Context, b StandingJSON) (StandingJSON, error) {
	q := url.Values{"agent": {b.Agent}, "to": {b.To}}
	if b.Stream != "" {
		q.Set("stream", b.Stream)
	}
	var out StandingJSON
	err := c.do(ctx, http.MethodDelete, "/v1/bindings?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}
