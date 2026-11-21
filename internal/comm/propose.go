package comm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
	l.proposals.add(stored.ID)
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets})
}

// closeTimeout bounds closing a pressed proposal's button.
const closeTimeout = 30 * time.Second

// OutcomePressed is what a proposal's copy shows once its button was pressed.
const OutcomePressed = "pressed"

// closeProposal closes the button of the proposal whose copy on channel has the
// platform id native, once the operator pressed it: the command ran, and a second
// press would run it again. It runs in the background, so the press's answer does
// not wait on the platform. A press on anything else, such as a gate question,
// which the gate closes itself, is left alone.
func (l *Layer) closeProposal(channel, native string) {
	if native == "" {
		return
	}
	n, ok := l.channelIDs[channel]
	if !ok {
		return
	}
	id, ok := l.composites.id(FormatComposite(n, native))
	if !ok || !l.proposals.take(id) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := l.CloseActions(ctx, id, OutcomePressed); err != nil {
			slog.Warn("a pressed proposal's button could not be closed", "message", id, "error", err)
		}
	}()
}

// maxProposals is how many open proposals are kept for closing. Past it the
// oldest is forgotten; its button then stays, as a typed command would.
const maxProposals = 256

// proposalSet is the ids of proposals whose button is still open.
type proposalSet struct {
	mu    sync.Mutex
	open  map[string]bool
	order []string
}

func (s *proposalSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		s.open = map[string]bool{}
	}
	s.open[id] = true
	s.order = append(s.order, id)
	for len(s.order) > maxProposals {
		delete(s.open, s.order[0])
		s.order = s.order[1:]
	}
}

// take reports whether id is an open proposal, and closes it in the set.
func (s *proposalSet) take(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open[id] {
		return false
	}
	delete(s.open, id)
	return true
}
