package local

import (
	"context"
	"net/http"
	"time"
)

// MeshPeerJSON is what a node knows about one peer.
type MeshPeerJSON struct {
	ID       string    `json:"id"`
	Addr     string    `json:"addr"`
	Up       bool      `json:"up"`
	LastBeat time.Time `json:"last_beat"`
	Role     string    `json:"role"`
	Epoch    uint64    `json:"epoch"`
	Master   string    `json:"master"`
}

// MeshJSON is a node's view of the mesh.
type MeshJSON struct {
	Node   string         `json:"node"`
	Role   string         `json:"role"`
	Epoch  uint64         `json:"epoch"`
	Master string         `json:"master"`
	Peers  []MeshPeerJSON `json:"peers"`
}

// WithMesh serves the node's view of the mesh at GET /v1/mesh. Without it the
// route is absent: a node that is not part of a mesh has nothing to show.
func WithMesh(status func() MeshJSON) Option { return func(s *Server) { s.meshStatus = status } }

func (s *Server) registerMesh(mux *http.ServeMux) {
	if s.meshStatus == nil {
		return
	}
	mux.HandleFunc("GET /v1/mesh", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.meshStatus())
	}))
}

// Mesh returns the node's view of the mesh.
func (c *Client) Mesh(ctx context.Context) (MeshJSON, error) {
	var out MeshJSON
	err := c.do(ctx, http.MethodGet, "/v1/mesh", nil, http.StatusOK, &out)
	return out, err
}
