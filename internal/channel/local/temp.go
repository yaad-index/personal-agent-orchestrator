package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
)

// Errors a TempAPI returns, which the server turns into status codes. The
// implementation wraps its own errors in these.
var (
	// ErrNotFound is a 404: no such agent, recipe or harvest.
	ErrNotFound = errors.New("not found")
	// ErrConflict is a 409: the name is taken, or its files are still there.
	ErrConflict = errors.New("conflict")
	// ErrBadRequest is a 400.
	ErrBadRequest = errors.New("bad request")
	// ErrNotReady is a 502: the agent was created but did not become ready.
	ErrNotReady = errors.New("agent not ready")
	// ErrForbidden is a 403.
	ErrForbidden = errors.New("forbidden")
)

// AgentJSON is one temporary agent as listed.
type AgentJSON struct {
	Name    string `json:"name"`
	Recipe  string `json:"recipe"`
	Channel string `json:"channel"`
	// Node is the node that holds the agent; empty is the node asked.
	Node         string    `json:"node,omitempty"`
	State        string    `json:"state"`
	Flag         string    `json:"flag"`
	Created      time.Time `json:"created"`
	LastActivity time.Time `json:"last_activity"`
}

// KillJSON is what a kill reports.
type KillJSON struct {
	Harvest     harvest.Manifest `json:"harvest"`
	WorkdirKept bool             `json:"workdir_kept"`
}

// SpawnRequestJSON asks for a temporary agent.
type SpawnRequestJSON struct {
	Recipe  string `json:"recipe"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	// Node is the node to start it on; empty is this node.
	Node string `json:"node,omitempty"`
}

// SpawnJSON is the answer to a spawn.
type SpawnJSON struct {
	Name string `json:"name"`
}

// TempAPI is the operator's temporary-agent surface. The server calls it on
// behalf of the operator, the only caller the bearer token identifies.
type TempAPI interface {
	// Spawn returns the agent's name; on ErrNotReady it still returns the name.
	// node is the node to start it on; empty is this node.
	Spawn(ctx context.Context, recipe, name, channel, node string) (string, error)
	Kill(ctx context.Context, name string) (KillJSON, error)
	Agents() []AgentJSON
	// Harvests lists those held on node; empty is this node.
	Harvests(ctx context.Context, node string) ([]harvest.Manifest, error)
	// A harvest on another node has an id qualified by it, as "node:id".
	Harvest(ctx context.Context, id string) (harvest.Manifest, error)
	HarvestFile(ctx context.Context, id, path string) ([]byte, error)
	DeleteHarvest(ctx context.Context, id string) error
}

// WithTemp serves the temporary-agent routes.
func WithTemp(t TempAPI) Option { return func(s *Server) { s.temp = t } }

func (s *Server) registerTemp(mux *http.ServeMux) {
	if s.temp == nil {
		return
	}
	mux.HandleFunc("POST /v1/temp", s.auth(s.spawn))
	mux.HandleFunc("GET /v1/temp", s.auth(s.listTemp))
	mux.HandleFunc("DELETE /v1/temp/{name}", s.auth(s.killTemp))
	mux.HandleFunc("GET /v1/harvests", s.auth(s.listHarvests))
	mux.HandleFunc("GET /v1/harvests/{id}", s.auth(s.getHarvest))
	mux.HandleFunc("GET /v1/harvests/{id}/file", s.auth(s.getHarvestFile))
	mux.HandleFunc("DELETE /v1/harvests/{id}", s.auth(s.deleteHarvest))
}

func tempStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotReady):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) spawn(w http.ResponseWriter, r *http.Request) {
	var req SpawnRequestJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Recipe == "" {
		writeError(w, http.StatusBadRequest, "a spawn request names a recipe")
		return
	}
	name, err := s.temp.Spawn(r.Context(), req.Recipe, req.Name, req.Channel, req.Node)
	if err != nil {
		if name != "" {
			// Created but not ready: the caller needs the name to inspect or kill it.
			writeJSON(w, tempStatus(err), map[string]string{"error": err.Error(), "name": name})
			return
		}
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, SpawnJSON{Name: name})
}

func (s *Server) listTemp(w http.ResponseWriter, _ *http.Request) {
	agents := s.temp.Agents()
	if agents == nil {
		agents = []AgentJSON{}
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) killTemp(w http.ResponseWriter, r *http.Request) {
	res, err := s.temp.Kill(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listHarvests(w http.ResponseWriter, r *http.Request) {
	hs, err := s.temp.Harvests(r.Context(), r.URL.Query().Get("node"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if hs == nil {
		hs = []harvest.Manifest{}
	}
	writeJSON(w, http.StatusOK, hs)
}

func (s *Server) getHarvest(w http.ResponseWriter, r *http.Request) {
	m, err := s.temp.Harvest(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) getHarvestFile(w http.ResponseWriter, r *http.Request) {
	data, err := s.temp.HarvestFile(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *Server) deleteHarvest(w http.ResponseWriter, r *http.Request) {
	if err := s.temp.DeleteHarvest(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- client ---

func (c *Client) do(ctx context.Context, method, path string, body any, want int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		b, err := io.ReadAll(resp.Body)
		*raw = b
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Spawn asks the node for a temporary agent and returns its name. When the
// agent was created but did not become ready, the name comes back with the
// error, so it can be inspected or killed.
func (c *Client) Spawn(ctx context.Context, recipe, name, channel, node string) (string, error) {
	b, err := json.Marshal(SpawnRequestJSON{Recipe: recipe, Name: name, Channel: channel, Node: node})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/temp", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
	if resp.StatusCode == http.StatusCreated {
		return body.Name, nil
	}
	if body.Error == "" {
		body.Error = http.StatusText(resp.StatusCode)
	}
	return body.Name, fmt.Errorf("%d: %s", resp.StatusCode, body.Error)
}

// Kill tears a temporary agent down and returns what was harvested.
func (c *Client) Kill(ctx context.Context, name string) (KillJSON, error) {
	var out KillJSON
	err := c.do(ctx, http.MethodDelete, "/v1/temp/"+url.PathEscape(name), nil, http.StatusOK, &out)
	return out, err
}

// Agents lists the temporary agents.
func (c *Client) Agents(ctx context.Context) ([]AgentJSON, error) {
	var out []AgentJSON
	err := c.do(ctx, http.MethodGet, "/v1/temp", nil, http.StatusOK, &out)
	return out, err
}

// Harvests lists the harvests on node, or on the node asked when node is empty.
func (c *Client) Harvests(ctx context.Context, node string) ([]harvest.Manifest, error) {
	var out []harvest.Manifest
	path := "/v1/harvests"
	if node != "" {
		path += "?node=" + url.QueryEscape(node)
	}
	err := c.do(ctx, http.MethodGet, path, nil, http.StatusOK, &out)
	return out, err
}

// Harvest returns one harvest's manifest.
func (c *Client) Harvest(ctx context.Context, id string) (harvest.Manifest, error) {
	var out harvest.Manifest
	err := c.do(ctx, http.MethodGet, "/v1/harvests/"+url.PathEscape(id), nil, http.StatusOK, &out)
	return out, err
}

// HarvestFile returns one harvested file.
func (c *Client) HarvestFile(ctx context.Context, id, path string) ([]byte, error) {
	var out []byte
	err := c.do(ctx, http.MethodGet, "/v1/harvests/"+url.PathEscape(id)+"/file?path="+url.QueryEscape(path), nil, http.StatusOK, &out)
	return out, err
}

// DeleteHarvest removes a harvest.
func (c *Client) DeleteHarvest(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/harvests/"+url.PathEscape(id), nil, http.StatusNoContent, nil)
}
