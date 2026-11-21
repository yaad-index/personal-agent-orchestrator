package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// closeTimeout bounds closing a settled question's buttons.
const closeTimeout = 30 * time.Second

// gateNotifier puts the gate's questions to the operator, with their answers as
// actions, at the asking agent's stderr destinations (ADR-0029 point 4), and
// closes those actions once the question is settled (ADR-0027 points 1 and 6).
type gateNotifier struct {
	layer interface {
		NotifyAgent(ctx context.Context, agent string, m core.Message) (core.Message, error)
		CloseActions(ctx context.Context, id, outcome string) error
	}
}

var _ gate.Notifier = gateNotifier{}

// Announce sends the question. The handle is the stored message's id, set even
// when a channel failed, so the copies that were shown can still be closed.
func (n gateNotifier) Announce(ctx context.Context, q gate.Question) (string, error) {
	m, err := n.layer.NotifyAgent(ctx, q.Agent, core.Message{Sender: "pao", Text: q.Text, Actions: q.Actions})
	return m.ID, err
}

// Settled closes the question's buttons in the background: the decision does not
// wait on a platform.
func (n gateNotifier) Settled(handle, outcome string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := n.layer.CloseActions(ctx, handle, outcome); err != nil {
			slog.Warn("a settled question's buttons could not all be closed", "message", handle, "error", err)
		}
	}()
}
