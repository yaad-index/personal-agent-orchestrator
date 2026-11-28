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

// turn is what starts an agent's turn: the conversation the message came from,
// and the subject who sent it.
type turn struct {
	at        origin
	requester string
}

// claim is a turn set for a delivery, and what it replaced.
type claim struct {
	set, prev  turn
	isSet, had bool
}

// claimOrigin makes the conversation in came from the origin of each agent it is
// about to be delivered to (ADR-0029 point 2), before it is delivered: an agent may
// answer as soon as it reads the message. A message from a source that is not a
// channel, such as another agent or a peer node, starts a turn with no origin. It
// returns what it replaced, for settleOrigin. The turn also notes who sent the
// message, for what pao says to that subject alone (a proposal, ADR-0030).
func (l *Layer) claimOrigin(in Inbound, targets []core.Profile) map[string]claim {
	o, fromChannel := turn{at: origin{channel: in.Source, conversation: in.Conversation}, requester: in.Sender}, l.HasChannel(in.Source)
	l.mu.Lock()
	defer l.mu.Unlock()
	claims := make(map[string]claim, len(targets))
	for _, t := range targets {
		prev, had := l.origins[t.Name]
		claims[t.Name] = claim{set: o, isSet: fromChannel, prev: prev, had: had}
		if fromChannel {
			l.origins[t.Name] = o
		} else {
			delete(l.origins, t.Name)
		}
	}
	return claims
}

// settleOrigin puts back the origin of each agent the delivery failed for: a
// message the agent never read starts no turn. An origin something else moved
// since is left as it is.
func (l *Layer) settleOrigin(claims map[string]claim, results []route.Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range results {
		c, ok := claims[r.Agent]
		if r.Err == nil || !ok {
			continue
		}
		now, has := l.origins[r.Agent]
		if has != c.isSet || (has && now != c.set) {
			continue
		}
		if c.had {
			l.origins[r.Agent] = c.prev
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
	return slices.Contains(l.identity.Operators(l.providerOf(o.channel)), o.conversation) && l.readersMay(PolicyOperators, m)
}

// conversationName names a conversation the way the operator does (ADR-0029
// point 1): an admitted group by its name, a private conversation by the subject
// it belongs to.
func (l *Layer) conversationName(o origin) string {
	if g, ok := l.group(o.channel, o.conversation); ok {
		return "group:" + g.Name
	}
	if subject, ok := l.identity.Resolve(l.providerOf(o.channel), o.conversation); ok {
		return "private:" + subject + "@" + o.channel
	}
	return o.conversation
}
