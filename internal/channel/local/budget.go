package local

import (
	"context"
	"net/http"
	"time"
)

// BudgetWindowJSON is one of an account's limit windows.
type BudgetWindowJSON struct {
	// Left is the share of the window left, in percent.
	Left   float64   `json:"left"`
	Resets time.Time `json:"resets,omitzero"`
}

// BudgetJSON is one account's budget as last known (ADR-0035).
type BudgetJSON struct {
	Account string `json:"account"`
	// State is "unmetered", "readable" or "fault".
	State    string            `json:"state"`
	Week     *BudgetWindowJSON `json:"week,omitempty"`
	FiveHour *BudgetWindowJSON `json:"five_hour,omitempty"`
	ReadAt   time.Time         `json:"read_at,omitzero"`
	Fault    string            `json:"fault,omitempty"`
	// Retrying says why the last read failed for a reason that passes, while
	// the last good figure is kept; RetryAt is when it is read again.
	Retrying string    `json:"retrying,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	Since    time.Time `json:"since"`
	// Headroom says whether work may be dispatched to an agent on the account.
	Headroom bool `json:"headroom"`
}

// WithBudget serves every account's budget at GET /v1/budget. It is read only.
func WithBudget(status func() []BudgetJSON) Option { return func(s *Server) { s.budget = status } }

func (s *Server) registerBudget(mux *http.ServeMux) {
	if s.budget == nil {
		return
	}
	mux.HandleFunc("GET /v1/budget", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		out := s.budget()
		if out == nil {
			out = []BudgetJSON{}
		}
		writeJSON(w, http.StatusOK, out)
	}))
}

// Budget returns every account's budget.
func (c *Client) Budget(ctx context.Context) ([]BudgetJSON, error) {
	var out []BudgetJSON
	err := c.do(ctx, http.MethodGet, "/v1/budget", nil, http.StatusOK, &out)
	return out, err
}
