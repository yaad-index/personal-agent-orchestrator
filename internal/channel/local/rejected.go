package local

import (
	"context"
	"net/http"
	"time"
)

// RejectedJSON is one entry of the record of inbounds pao did not act on
// (ADR-0019 point 7). The payload is stranger content: it is for the operator to
// read, and goes nowhere else.
type RejectedJSON struct {
	Channel string    `json:"channel"`
	Sender  string    `json:"sender"`
	Name    string    `json:"name,omitempty"`
	Kind    string    `json:"kind"`
	Reason  string    `json:"reason"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Count   int       `json:"count"`
	Payload string    `json:"payload"`
}

// RejectedAPI is the operator's reading of the rejected-inbound record. There is
// no write and no way to act on an entry here: the record is inert (ADR-0020
// point 8).
type RejectedAPI interface {
	Rejected(ctx context.Context) ([]RejectedJSON, error)
}

// WithRejected serves the rejected-inbound listing.
func WithRejected(a RejectedAPI) Option { return func(s *Server) { s.rejected = a } }

func (s *Server) registerRejected(mux *http.ServeMux) {
	if s.rejected == nil {
		return
	}
	mux.HandleFunc("GET /v1/inbound/rejected", s.auth(s.listRejected))
}

func (s *Server) listRejected(w http.ResponseWriter, r *http.Request) {
	out, err := s.rejected.Rejected(r.Context())
	if err != nil {
		writeError(w, tempStatus(err), err.Error())
		return
	}
	if out == nil {
		out = []RejectedJSON{}
	}
	writeJSON(w, http.StatusOK, out)
}

// Rejected lists the rejected inbounds, the most recent first.
func (c *Client) Rejected(ctx context.Context) ([]RejectedJSON, error) {
	var out []RejectedJSON
	err := c.do(ctx, http.MethodGet, "/v1/inbound/rejected", nil, http.StatusOK, &out)
	return out, err
}
