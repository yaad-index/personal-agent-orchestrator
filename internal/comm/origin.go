package comm

import (
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// origin is a conversation: a channel and, on it, the platform's id for the
// conversation. An empty conversation is the channel's one conversation, as on the
// local channel. The origin of an agent's turn is the conversation the message
// that started it came from.
type origin struct {
	channel      string
	conversation string
}

// noteOrigin makes the conversation in came from the origin of each agent it was
// delivered to (ADR-0029 point 2). A message from a source that is not a channel,
// such as another agent or a peer node, starts a turn with no origin.
func (l *Layer) noteOrigin(in Inbound, results []route.Result) {
	o, fromChannel := origin{channel: in.Source, conversation: in.Conversation}, l.HasChannel(in.Source)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		if fromChannel {
			l.origins[r.Agent] = o
		} else {
			delete(l.origins, r.Agent)
		}
	}
}

// originMay reports whether every reader of a conversation may see m. An admitted
// group's readers are its members under its policy; a private conversation's one
// reader is the operator it belongs to. A conversation that is neither any more,
// such as a group no longer admitted, shows nothing.
func (l *Layer) originMay(o origin, m core.Message) bool {
	if g, ok := l.group(o.channel, o.conversation); ok {
		return l.readersMay(g.Policy, m)
	}
	return slices.Contains(l.identity.Operators(o.channel), o.conversation) && l.readersMay(PolicyOperators, m)
}

// conversationName names a conversation the way the operator does (ADR-0029
// point 1): an admitted group by its name, a private conversation by the subject
// it belongs to.
func (l *Layer) conversationName(o origin) string {
	if g, ok := l.group(o.channel, o.conversation); ok {
		return "group:" + g.Name
	}
	if subject, ok := l.identity.Resolve(o.channel, o.conversation); ok {
		return "private:" + subject + "@" + o.channel
	}
	return o.conversation
}
