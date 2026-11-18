package daemon

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// chatCommands is what pao's chat commands run (ADR-0026 point 2): the operator
// surface's own stream, view and gate operations, with the same checks, so a
// command can do nothing from chat that the operator could not do at the node.
type chatCommands struct {
	streams *streamAPI
	decider *gateDecider
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
