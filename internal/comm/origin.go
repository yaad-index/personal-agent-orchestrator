package comm

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// origin is the conversation a message delivered to an agent came from: the
// channel and, on it, the platform's id for the conversation. An empty
// conversation is the channel's one conversation, as on the local channel.
type origin struct {
	channel      string
	conversation string
}

// outRoute is where one message an agent sent is going, held while the channels
// are handed it so each can tell its conversations apart.
type outRoute struct {
	origin origin
	// boundTo is the channel the agent's output is bound to, if it is bound.
	boundTo string
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

// sendFrom hands m, an agent's output, to the channels it goes to.
//
// Output in a turn with an origin goes back to the origin's conversation, and to
// the channel the agent's output is bound to, if it is (ADR-0029 points 2 and 3).
//
// Output with no origin goes where an agent's output went before origins: to the
// channel it is bound to, or else to every channel. This is interim, until standing
// bindings are configured; then output with no origin goes only to them.
func (l *Layer) sendFrom(ctx context.Context, from string, m core.Message) error {
	l.mu.RLock()
	only, bound := l.bound[from]
	o, hasOrigin := l.origins[from]
	l.mu.RUnlock()
	if !hasOrigin {
		if bound {
			return l.sendTo(ctx, only, m)
		}
		return l.fanOut(ctx, m)
	}
	l.routes.Store(m.ID, outRoute{origin: o, boundTo: only})
	defer l.routes.Delete(m.ID)
	errs := []error{l.sendTo(ctx, o.channel, m)}
	if bound && only != o.channel {
		errs = append(errs, l.sendTo(ctx, only, m))
	}
	return errors.Join(errs...)
}

// toOrigin returns the conversations on channel that m, sent on route r, goes to:
// the origin's, if it is on this channel and every reader there may see m, and every
// conversation that may show m if the agent's output is bound to this channel. One
// conversation that is both gets m once.
func (l *Layer) toOrigin(channel string, m core.Message, r outRoute) []string {
	var out []string
	if r.boundTo == channel {
		out = l.wide(channel, m)
	}
	if r.origin.channel != channel || slices.Contains(out, r.origin.conversation) {
		return out
	}
	if !l.originMay(r.origin, m) {
		// Held back, never sent somewhere else (ADR-0029 point 6).
		slog.Warn("reply held back: a reader of the conversation it was addressed from may not see it",
			"agent", m.Sender, "channel", channel, "conversation", l.conversationName(r.origin), "message", l.Qualify(m.ID))
		return out
	}
	return append(out, r.origin.conversation)
}

// originMay reports whether every reader of the origin's conversation may see m.
// An admitted group's readers are its members under its policy; a private
// conversation's one reader is the operator it belongs to. A conversation that is
// neither any more, such as a group no longer admitted, shows nothing.
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
