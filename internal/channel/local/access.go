package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// GrantJSON is one grant of the access matrix: a subject ("operator" or
// "agent:<name>") given one resource by name, or every resource of the kind that
// carries a tag (ADR-0012 points 3 and 5).
type GrantJSON struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

// GrantsJSON is the grants, and the version of the stored document they come from.
type GrantsJSON struct {
	Version uint64      `json:"version"`
	Grants  []GrantJSON `json:"grants"`
}

// GrantChangeJSON says whether a grant or a revocation changed anything.
type GrantChangeJSON struct {
	Changed bool `json:"changed"`
}

// GrantsAPI is the operator's administration of the access matrix, changed while
// the node runs. It is the API half of the management surface (ADR-0005).
type GrantsAPI interface {
	// Grants lists the grants, of one subject when subject is not empty.
	Grants(ctx context.Context, subject string) (GrantsJSON, error)
	AddGrant(ctx context.Context, g GrantJSON) (GrantChangeJSON, error)
	RemoveGrant(ctx context.Context, g GrantJSON) (GrantChangeJSON, error)
}

// WithGrants serves the access-matrix routes.
func WithGrants(a GrantsAPI) Option { return func(s *Server) { s.grants = a } }

func (s *Server) registerGrants(mux *http.ServeMux) {
	if s.grants == nil {
		return
	}
	mux.HandleFunc("GET /v1/access/grants", s.auth(s.listGrants))
	mux.HandleFunc("POST /v1/access/grants", s.auth(s.addGrant))
	mux.HandleFunc("DELETE /v1/access/grants", s.auth(s.removeGrant))
}

func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	out, err := s.grants.Grants(r.Context(), r.URL.Query().Get("subject"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out.Grants == nil {
		out.Grants = []GrantJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addGrant(w http.ResponseWriter, r *http.Request) {
	var g GrantJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "a grant names a subject, a kind, and a name or a tag")
		return
	}
	s.changeGrant(w, r, s.grants.AddGrant, g)
}

func (s *Server) removeGrant(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.changeGrant(w, r, s.grants.RemoveGrant, GrantJSON{Subject: q.Get("subject"), Kind: q.Get("kind"), Name: q.Get("name"), Tag: q.Get("tag")})
}

func (s *Server) changeGrant(w http.ResponseWriter, r *http.Request, f func(context.Context, GrantJSON) (GrantChangeJSON, error), g GrantJSON) {
	out, err := f(r.Context(), g)
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Grants lists the grants, of one subject when subject is not empty.
func (c *Client) Grants(ctx context.Context, subject string) (GrantsJSON, error) {
	path := "/v1/access/grants"
	if subject != "" {
		path += "?subject=" + url.QueryEscape(subject)
	}
	var out GrantsJSON
	err := c.do(ctx, http.MethodGet, path, nil, http.StatusOK, &out)
	return out, err
}

// AddGrant adds a grant.
func (c *Client) AddGrant(ctx context.Context, g GrantJSON) (GrantChangeJSON, error) {
	var out GrantChangeJSON
	err := c.do(ctx, http.MethodPost, "/v1/access/grants", g, http.StatusOK, &out)
	return out, err
}

// RemoveGrant revokes a grant.
func (c *Client) RemoveGrant(ctx context.Context, g GrantJSON) (GrantChangeJSON, error) {
	q := url.Values{"subject": {g.Subject}, "kind": {g.Kind}}
	if g.Name != "" {
		q.Set("name", g.Name)
	}
	if g.Tag != "" {
		q.Set("tag", g.Tag)
	}
	var out GrantChangeJSON
	err := c.do(ctx, http.MethodDelete, "/v1/access/grants?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}
