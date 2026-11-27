package local

import (
	"context"
	"net/http"
)

// NodeJSON is what the node is: its name, its build and the agents it holds.
type NodeJSON struct {
	Node   string          `json:"node"`
	Build  string          `json:"build"`
	Agents []NodeAgentJSON `json:"agents"`
}

// NodeAgentJSON is one of the node's agents.
type NodeAgentJSON struct {
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	Backend string   `json:"backend"`
	// State is "running" or "stopped".
	State string `json:"state"`
	// Temporary says the agent was spawned from a recipe (ADR-0006).
	Temporary bool `json:"temporary"`
}

// Node reads what the node is and the agents it holds.
func (c *Client) Node(ctx context.Context) (NodeJSON, error) {
	var out NodeJSON
	err := c.do(ctx, http.MethodGet, "/v1/node", nil, http.StatusOK, &out)
	return out, err
}
