package comm

import (
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Scope is what a chat channel is: one conversation, and the one subject whose
// inbound there is acted on (ADR-0045 point 6, ADR-0038 point 3).
type Scope struct {
	// Owner is the subject the channel belongs to.
	Owner string
	// Conversation is the platform's id for the channel's group conversation,
	// or for a topic of one as TopicConversation forms it. Empty means the
	// channel is the owner's private conversation with it.
	Conversation string
}

// SetScopes gives the layer each chat channel's scope, by channel name. A chat
// channel with no scope acts on nothing and shows nothing. Call it before the
// layer starts receiving.
func (l *Layer) SetScopes(scopes map[string]Scope) { l.scopes = scopes }

// outOfScope reports why an inbound is not the channel's to act on, or "" if
// it is: it came from the channel's one conversation, from its owner. A
// registration is pao's own and is let through: in a private conversation it is
// the one way an owner binds a sender id, one from a group is refused further
// on, and neither is ever routed to an agent.
// The platform's reports that a group moved or someone joined are recorded
// whatever the group, so they are let through too.
func (l *Layer) outOfScope(in ChannelInbound) string {
	if in.MovedTo != "" || in.Join || in.Registration || l.unscoped {
		return ""
	}
	s, ok := l.scopes[in.Channel]
	if !ok {
		return "channel has no owner and conversation"
	}
	// pao's own questions and notices also go to the owner privately
	// (inScope), so a press there, which only ever answers one, is the
	// channel's too.
	privatePress := in.Press && !in.Group
	switch {
	case s.Conversation == "" && in.Group:
		return "not the channel's conversation: " + conversationOf(in)
	case s.Conversation != "" && !privatePress && (!in.Group || in.Conversation != s.Conversation):
		return "not the channel's conversation: " + conversationOf(in)
	}
	subject, ok := l.identity.Resolve(in.Channel, in.Sender)
	switch {
	case !ok:
		return "no subject"
	case subject != s.Owner:
		return "not the channel's owner"
	}
	return ""
}

func conversationOf(in ChannelInbound) string {
	switch {
	case !in.Group:
		return "private"
	case in.Conversation == "":
		return "unknown"
	}
	return in.Conversation
}

// inScope narrows where a channel shows a message to the channel's one
// conversation: of the conversations the layer chose on the channel, only that
// one is kept. Where the channel is a topic, its group stands for it, since a
// group's output with no origin is chosen by the group's id. pao's own
// messages, a gate question among them, may also go to the owner privately,
// where the layer sends them whatever the channel's conversation: no one but
// the owner reads them there. A channel with no scope shows nothing.
func (l *Layer) inScope(channel string, m core.Message, out []string) []string {
	if l.unscoped {
		return out
	}
	if len(out) == 0 {
		return nil
	}
	s, ok := l.scopes[channel]
	if !ok {
		return nil
	}
	var own []string
	if s.Conversation == "" || m.Kind == core.MessageSystemEvent {
		own = l.identity.Senders(s.Owner, channel)
	}
	group, _, topic := SplitTopic(s.Conversation)
	var kept []string
	for _, c := range out {
		switch {
		case s.Conversation != "" && (c == s.Conversation || topic && c == group):
			c = s.Conversation
		case !slices.Contains(own, c):
			continue
		}
		if !slices.Contains(kept, c) {
			kept = append(kept, c)
		}
	}
	return kept
}
