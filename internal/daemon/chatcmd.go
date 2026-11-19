package daemon

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// chatCommands is what pao's chat commands run (ADR-0026 point 2): the operator
// surface's own stream, view and gate operations, with the same checks, so a
// command can do nothing from chat that the operator could not do at the node.
type chatCommands struct {
	streams *streamAPI
	decider *gateDecider
	// agents is the node's agent list: this node's agents and, on a master, those
	// in the mesh directory.
	agents interface{ Agents() []mcpsrv.AgentInfo }
	auth   access.Authorizer
}

var _ command.Ops = chatCommands{}

func (c chatCommands) SetStream(ctx context.Context, agent, channel string, level int) error {
	return c.streams.SetStream(ctx, agent, channel, level)
}

func (c chatCommands) View(ctx context.Context, agent, action string) error {
	return c.streams.View(ctx, agent, action, 0, 0)
}

func (c chatCommands) Decide(_ context.Context, id, decision, reason string) error {
	ans, ok := gate.ParseAnswer(decision, reason)
	if !ok {
		return fmt.Errorf(`decision must be "allow", "always" or "deny", not %q`, decision)
	}
	return c.decider.Answer(id, ans)
}

// Agents lists the agents the operator may see, with the same check the agent
// list over MCP makes for the operator.
func (c chatCommands) Agents(context.Context) ([]command.Agent, error) {
	var out []command.Agent
	for _, a := range c.agents.Agents() {
		if !access.Allowed(c.auth, access.Operator, access.List, access.AgentResource(a.Name, a.Tags)) {
			continue
		}
		out = append(out, command.Agent{Name: a.Name, Tags: a.Tags, State: a.State})
	}
	return out, nil
}
