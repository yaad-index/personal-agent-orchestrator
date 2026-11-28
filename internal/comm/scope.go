package comm

import (
	"log/slog"
	"maps"
	"slices"
	"strings"

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
	subject, ok := l.identity.Resolve(l.providerOf(in.Channel), in.Sender)
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
	if len(out) == 0 || l.othersOutput(channel, m) {
		return nil
	}
	s, ok := l.scopes[channel]
	if !ok {
		return nil
	}
	var own []string
	if s.Conversation == "" || m.Kind == core.MessageSystemEvent {
		own = l.identity.Senders(s.Owner, l.providerOf(channel))
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

// ReplyNoAgent answers a message on a channel that has no agent yet.
const ReplyNoAgent = "this channel has no agent yet: set its default agent."

// channelAgent is the agent a scoped channel belongs to: the default agent of
// its one conversation (ADR-0058 point 4).
func (l *Layer) channelAgent(channel string) (string, bool) {
	s, ok := l.scopes[channel]
	if !ok {
		return "", false
	}
	return l.DefaultAgentIn(channel, s.Conversation)
}

// onlyItsAgent returns the answer to a message on a channel whose target is not
// the channel's own agent, or "" if it is. A channel with no agent reaches none,
// and an address naming another agent, or a tag reaching more than its own, is
// refused rather than narrowed, so the sender learns it went nowhere.
func (l *Layer) onlyItsAgent(channel string, target core.Target) string {
	if l.unscoped {
		return ""
	}
	agent, ok := l.channelAgent(channel)
	if !ok {
		return ReplyNoAgent
	}
	ps, err := l.router.Resolve(target)
	if err == nil && len(ps) == 1 && ps[0].Name == agent {
		return ""
	}
	return "only " + agent + " is reached from this channel."
}

// othersOutput reports whether m is the words of an agent other than the
// channel's own, which a channel with an agent never shows (ADR-0058 point 8).
// pao's own messages, a person's words mirrored, and the governor's answers
// are not. A channel with no agent shows what is bound to it, as a log channel
// would (ADR-0058 point 7).
func (l *Layer) othersOutput(channel string, m core.Message) bool {
	if m.Kind == core.MessageSystemEvent || m.Sender == "" || m.Sender == l.governor {
		return false
	}
	agent, ok := l.channelAgent(channel)
	if !ok || m.Sender == agent {
		return false
	}
	ps, err := l.router.Resolve(core.Target{Kind: core.ByName, Value: m.Sender})
	if err != nil || len(ps) == 0 {
		return false
	}
	if _, seen := l.unshown.LoadOrStore("other\x00"+channel+"\x00"+m.Sender, true); !seen {
		slog.Warn("an agent's output was held back from a channel that is another agent's", "channel", channel, "agent", m.Sender)
	}
	return true
}

// SetProviders gives the layer the provider each chat channel is carried on,
// by channel name (ADR-0058 point 1). A sender's id belongs to the platform
// connection, so a channel's senders are bound to subjects by its provider. A
// channel given no provider is its own. Call it before the layer starts
// receiving.
func (l *Layer) SetProviders(providers map[string]string) { l.providers = providers }

// providerOf is the namespace a channel's senders are bound in: its provider's
// name, or its own where it has none.
func (l *Layer) providerOf(channel string) string {
	if p, ok := l.providers[channel]; ok {
		return p
	}
	return channel
}

// place puts an inbound a provider handed over without a channel into the
// provider's channel it belongs to (ADR-0058 point 2): the channel whose group
// or topic it was written in, or, in a private conversation, the private
// channel of the subject the sender is bound to. It reports false where none
// is, and the inbound is then the provider's own, recorded under its name.
func (l *Layer) place(in ChannelInbound) (string, bool) {
	var subject string
	if !in.Group {
		subject, _ = l.identity.Resolve(in.Provider, in.Sender)
	}
	group, _, _ := strings.Cut(in.Conversation, "/")
	var near string
	for _, ch := range slices.Sorted(maps.Keys(l.scopes)) {
		s := l.scopes[ch]
		if l.providerOf(ch) != in.Provider {
			continue
		}
		switch {
		case !in.Group && s.Conversation == "" && subject != "" && s.Owner == subject:
			return ch, true
		case in.Press && !in.Group && subject != "" && s.Owner == subject:
			// pao asks its owner privately, whatever the channel's conversation:
			// a press there is the owner's channel's, the one that showed the
			// message if it can be told, and otherwise the first of them
			if n, ok := l.channelIDs[ch]; ok && in.Pressed != "" {
				if _, shown := l.composites.id(FormatComposite(n, in.Pressed)); shown {
					return ch, true
				}
			}
			if near == "" {
				near = ch
			}
		case in.Group && s.Conversation == in.Conversation:
			return ch, true
		case in.Group && (in.Join || in.MovedTo != "") && strings.HasPrefix(s.Conversation, group+"/"):
			// the platform reports a move or a join for the group as a whole
			near = ch
		}
	}
	return near, near != ""
}
