package daemon

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// chatCommands is what pao's chat commands run (ADR-0026 point 2): the operator
// surface's own stream, binding, view, temporary-agent and gate operations, with the same checks, so a
// command can do nothing from chat that the operator could not do at the node.
type chatCommands struct {
	streams  *streamAPI
	standing *standingAPI
	temps    *tempAPI
	decider  *gateDecider
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

func (c chatCommands) Bind(ctx context.Context, b command.Binding) (command.Binding, error) {
	set, err := c.standing.Bind(ctx, local.StandingJSON{Agent: b.Agent, Stream: b.Stream, To: b.To, Level: b.Level, Prefix: b.Prefix})
	return commandBinding(set), err
}

func (c chatCommands) Unbind(ctx context.Context, b command.Binding) (command.Binding, error) {
	gone, err := c.standing.Unbind(ctx, local.StandingJSON{Agent: b.Agent, Stream: b.Stream, To: b.To})
	return commandBinding(gone), err
}

func (c chatCommands) Bindings(ctx context.Context, agent string) ([]command.Binding, error) {
	list, err := c.standing.Bindings(ctx, agent)
	if err != nil {
		return nil, err
	}
	out := make([]command.Binding, 0, len(list.Bindings))
	for _, b := range list.Bindings {
		out = append(out, commandBinding(b))
	}
	return out, nil
}

func commandBinding(b local.StandingJSON) command.Binding {
	return command.Binding{Agent: b.Agent, Stream: b.Stream, To: b.To, Level: b.Level, Prefix: b.Prefix, From: b.From}
}

// Spawn is the operator surface's spawn, on this node.
func (c chatCommands) Spawn(ctx context.Context, recipe, name, channel string) (string, error) {
	return c.temps.Spawn(ctx, recipe, name, channel, "", nil)
}

// Kill is the operator surface's kill: on this node, or on the node of the mesh
// that holds the agent. The answer carries the harvest report where this node
// has it: a kill on another node reports only the harvest's id.
func (c chatCommands) Kill(ctx context.Context, name string) (command.Killed, error) {
	here := c.temps.dir == nil || c.temps.holds(name)
	k, err := c.temps.Kill(ctx, name)
	if err != nil {
		return command.Killed{}, err
	}
	out := command.Killed{Harvest: k.Harvest.ID, WorkdirKept: k.WorkdirKept}
	if here {
		out.Report = k.Harvest.Summary()
	}
	return out, nil
}
