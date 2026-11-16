package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// SubjectJSON is a human subject as the operator sees it.
type SubjectJSON struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

// BindingJSON ties one (channel, per-sender id) pair to a subject.
type BindingJSON struct {
	Channel string `json:"channel"`
	Sender  string `json:"sender"`
	Subject string `json:"subject"`
}

// IdentityAPI is the operator's view of who the humans are and which sender ids
// are theirs (ADR-0019). Only the operator, the one caller the bearer token
// identifies, reaches it. An error wraps ErrNotFound, ErrConflict or
// ErrBadRequest for the status it should have.
type IdentityAPI interface {
	AddSubject(ctx context.Context, name string) error
	RemoveSubject(ctx context.Context, name string) error
	Subjects(ctx context.Context) ([]SubjectJSON, error)
	Bind(ctx context.Context, b BindingJSON) error
	Revoke(ctx context.Context, channel, sender string) error
	Bindings(ctx context.Context, subject string) ([]BindingJSON, error)
}

// WithIdentity serves the identity routes.
func WithIdentity(a IdentityAPI) Option { return func(s *Server) { s.identity = a } }

func (s *Server) registerIdentity(mux *http.ServeMux) {
	if s.identity == nil {
		return
	}
	mux.HandleFunc("GET /v1/identity/subjects", s.auth(s.listSubjects))
	mux.HandleFunc("POST /v1/identity/subjects", s.auth(s.addSubject))
	mux.HandleFunc("DELETE /v1/identity/subjects/{name}", s.auth(s.removeSubject))
	mux.HandleFunc("GET /v1/identity/bindings", s.auth(s.listBindings))
	mux.HandleFunc("POST /v1/identity/bindings", s.auth(s.bind))
	// The sender id is opaque and may hold a slash, so it travels in the query.
	mux.HandleFunc("DELETE /v1/identity/bindings", s.auth(s.revoke))
}

func (s *Server) listSubjects(w http.ResponseWriter, r *http.Request) {
	out, err := s.identity.Subjects(r.Context())
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out == nil {
		out = []SubjectJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addSubject(w http.ResponseWriter, r *http.Request) {
	var req SubjectJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	if err := s.identity.AddSubject(r.Context(), req.Name); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeSubject(w http.ResponseWriter, r *http.Request) {
	if err := s.identity.RemoveSubject(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listBindings(w http.ResponseWriter, r *http.Request) {
	out, err := s.identity.Bindings(r.Context(), r.URL.Query().Get("subject"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out == nil {
		out = []BindingJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) bind(w http.ResponseWriter, r *http.Request) {
	var req BindingJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	if err := s.identity.Bind(r.Context(), req); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.identity.Revoke(r.Context(), q.Get("channel"), q.Get("sender")); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Subjects lists the human subjects.
func (c *Client) Subjects(ctx context.Context) ([]SubjectJSON, error) {
	var out []SubjectJSON
	err := c.do(ctx, http.MethodGet, "/v1/identity/subjects", nil, http.StatusOK, &out)
	return out, err
}

// AddSubject creates a human subject.
func (c *Client) AddSubject(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/identity/subjects", SubjectJSON{Name: name}, http.StatusNoContent, nil)
}

// RemoveSubject deletes a subject and its bindings.
func (c *Client) RemoveSubject(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/identity/subjects/"+url.PathEscape(name), nil, http.StatusNoContent, nil)
}

// Bindings lists the bindings, of one subject when subject is not empty.
func (c *Client) Bindings(ctx context.Context, subject string) ([]BindingJSON, error) {
	var out []BindingJSON
	p := "/v1/identity/bindings"
	if subject != "" {
		p += "?subject=" + url.QueryEscape(subject)
	}
	err := c.do(ctx, http.MethodGet, p, nil, http.StatusOK, &out)
	return out, err
}

// Bind ties a (channel, sender) pair to a subject.
func (c *Client) Bind(ctx context.Context, b BindingJSON) error {
	return c.do(ctx, http.MethodPost, "/v1/identity/bindings", b, http.StatusNoContent, nil)
}

// Revoke removes a binding.
func (c *Client) Revoke(ctx context.Context, channel, sender string) error {
	q := url.Values{"channel": {channel}, "sender": {sender}}
	return c.do(ctx, http.MethodDelete, "/v1/identity/bindings?"+q.Encode(), nil, http.StatusNoContent, nil)
}
