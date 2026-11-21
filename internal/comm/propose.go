package comm

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Propose sends pao's message m, a command proposed on an agent's behalf, to the
// subject who sent the message that started the agent's current turn, and to
// that subject alone (ADR-0030 point 3): to the subject's private conversations on
// the channel the message came from, or to that channel's one conversation, as on
// the local channel. Like Notify, m is stored in no agent's scope.
//
// A turn not started from a channel, such as one started by another agent, has no
// one to propose to, and nor does a subject who is not an operator: both are an
// error, and nothing is sent.
func (l *Layer) Propose(ctx context.Context, agent string, m core.Message) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	l.mu.RLock()
	t, ok := l.origins[agent]
	l.mu.RUnlock()
	if !ok || t.requester == "" {
		return core.Message{}, fmt.Errorf("agent %q: its turn was not started from a channel, so there is no one to propose to", agent)
	}
	var targets []target
	if t.at.conversation == "" {
		targets = append(targets, target{kind: toProposal, at: t.at, agent: agent})
	} else if l.identity != nil {
		for _, s := range l.identity.Senders(t.requester, t.at.channel) {
			at := origin{channel: t.at.channel, conversation: s}
			if l.noticeMay(at, m) {
				targets = append(targets, target{kind: toProposal, at: at, agent: agent})
			}
		}
	}
	if len(targets) == 0 {
		return core.Message{}, fmt.Errorf("agent %q: %s has no private conversation an operator reads on %s", agent, t.requester, t.at.channel)
	}
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets})
}
