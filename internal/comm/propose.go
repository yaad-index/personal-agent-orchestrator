package comm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Propose sends pao's message m, a command proposed on an agent's behalf, back to
// the conversation the message that started the agent's current turn came from
// (ADR-0034 point 4): a group's when it was asked in one, a private one when it
// was asked privately, the local channel's one conversation from there. Like
// Notify, m is stored in no agent's scope.
//
// A turn not started from a channel, such as one started by another agent, has no
// one to propose to, and a conversation whose readers may not see it is not sent
// it: both are an error, and nothing is sent, never anywhere else.
func (l *Layer) Propose(ctx context.Context, agent string, m core.Message) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	l.mu.RLock()
	t, ok := l.origins[agent]
	l.mu.RUnlock()
	if !ok || t.requester == "" {
		return core.Message{}, fmt.Errorf("agent %q: its turn was not started from a channel, so there is no one to propose to", agent)
	}
	if !l.noticeMay(t.at, m) {
		return core.Message{}, fmt.Errorf("agent %q: a reader of %s, where it was asked, may not see the proposal", agent, l.conversationName(t.at))
	}
	targets := []target{{kind: toOrigin, at: t.at, agent: agent}}
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	l.proposals.add(stored.ID)
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets})
}

// toSubject is where a message for subject alone goes on channel: the subject's
// private conversations there that pass the reader rule, or, on a channel of one
// conversation such as the local channel, that conversation.
func (l *Layer) toSubject(channel string, one bool, subject, agent string, m core.Message) []target {
	var out []target
	for _, t := range l.subjectTargets(channel, one, subject, agent) {
		if l.noticeMay(t.at, m) {
			out = append(out, t)
		}
	}
	return out
}

// subjectTargets is every conversation of subject alone on channel, before the
// reader rule, which routing applies again to each message.
func (l *Layer) subjectTargets(channel string, one bool, subject, agent string) []target {
	if one {
		return []target{{kind: toSubject, at: origin{channel: channel}, agent: agent}}
	}
	if l.identity == nil {
		return nil
	}
	var out []target
	for _, s := range l.identity.Senders(subject, l.providerOf(channel)) {
		out = append(out, target{kind: toSubject, at: origin{channel: channel, conversation: s}, agent: agent})
	}
	return out
}

// Tell sends pao's message m to subject alone, on channel: the later answer to a
// command the subject sent there (ADR-0026 point 5), such as the outcome of a
// spawn. It goes to the subject's private conversations on the channel, never to
// a group, even when the command was sent in one. On a channel of one
// conversation, such as the local channel, subject is empty and m goes to that
// conversation. Like Notify, m is stored in no agent's scope.
func (l *Layer) Tell(ctx context.Context, channel, subject string, m core.Message) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	targets := l.toSubject(channel, subject == "", subject, "", m)
	if len(targets) == 0 {
		return core.Message{}, fmt.Errorf("%s has no private conversation an operator reads on %s", subject, channel)
	}
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
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
