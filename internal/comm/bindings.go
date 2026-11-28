package comm

import (
	"log/slog"
	"slices"
)

// The kinds of destination (ADR-0029 point 1).
const (
	DestGroup   = "group"
	DestPrivate = "private"
	DestChannel = "channel"
)

// The streams a binding follows (ADR-0023 point 1).
const (
	// StreamStdout is the agent's output.
	StreamStdout = "stdout"
	// StreamStderr is pao's gate questions and notices about the agent
	// (ADR-0029 point 4).
	StreamStderr = "stderr"
)

// Dest is a destination by name: an admitted group by its name, an operator's
// private conversation by the subject (and, where it has bindings on several
// channels, the channel), or a channel with one conversation by the channel's name.
type Dest struct {
	Kind    string
	Name    string
	Channel string
}

// String is the destination's name.
func (d Dest) String() string {
	s := d.Kind + ":" + d.Name
	if d.Channel != "" {
		s += "@" + d.Channel
	}
	return s
}

// Binding is a standing binding: To follows one of the agent's streams (ADR-0029
// point 3). A stdout binding carries all of the agent's output at Level, whichever
// conversation a turn began in; Prefix, when set, is shown before each message in
// place of the agent's name (point 5). A stderr binding carries pao's gate
// questions and notices about the agent (point 4), and has no level or prefix. An
// empty Stream is stdout.
type Binding struct {
	Agent  string
	Stream string
	To     Dest
	Level  int
	Prefix string
}

// SetBindings gives the layer the node's standing bindings. Call it before the
// layer starts sending.
func (l *Layer) SetBindings(bs []Binding) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bindings = slices.Clone(bs)
}

// bindingsOf returns the agent's standing bindings of a stream. Its stdout
// includes its log channel, unless that is paused.
func (l *Layer) bindingsOf(agent, stream string) []Binding {
	var out []Binding
	if stream == StreamStdout {
		if b, ok := l.logBinding(agent); ok {
			out = append(out, b)
		}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, b := range l.bindings {
		s := b.Stream
		if s == "" {
			s = StreamStdout
		}
		if b.Agent == agent && s == stream {
			out = append(out, b)
		}
	}
	return out
}

// resolve returns the conversations a destination names now. A group no longer
// admitted, a subject with no binding, or a subject on several channels when the
// name does not say which, names none, and is logged once.
func (l *Layer) resolve(d Dest) []origin {
	switch d.Kind {
	case DestGroup:
		for ch, gs := range l.groups {
			for id, g := range gs {
				if g.Name == d.Name && id != "" {
					return []origin{{channel: ch, conversation: id}}
				}
			}
		}
	case DestPrivate:
		var out []origin
		channels := l.Channels()
		if d.Channel != "" {
			channels = []string{d.Channel}
		}
		on := 0
		for _, ch := range channels {
			senders := l.identity.Senders(d.Name, l.providerOf(ch))
			if len(senders) > 0 {
				on++
			}
			for _, s := range senders {
				out = append(out, origin{channel: ch, conversation: s})
			}
		}
		if on > 1 {
			l.warnUnresolved(d, "the subject is on several channels: name one")
			return nil
		}
		if len(out) > 0 {
			return out
		}
	case DestChannel:
		if l.HasChannel(d.Name) {
			// A chat channel whose one conversation is a group or a topic is
			// that conversation (ADR-0058 point 2).
			if s, ok := l.scopes[d.Name]; ok && s.Conversation != "" {
				return []origin{{channel: d.Name, conversation: s.Conversation}}
			}
			return []origin{{channel: d.Name}}
		}
	}
	l.warnUnresolved(d, "it names no conversation")
	return nil
}

// warnSameConversation logs, once per pair, that two of an agent's bindings on a
// stream name the same conversation, such as private:alice and private:alice@chat.
// The conversation is shown one copy, the first binding's; the second does
// nothing.
func (l *Layer) warnSameConversation(agent, stream, first, second string) {
	if first == second {
		return
	}
	if _, seen := l.unshown.LoadOrStore("same\x00"+agent+"\x00"+stream+"\x00"+first+"\x00"+second, true); seen {
		return
	}
	slog.Warn("two bindings name the same conversation: the second sends nothing", "agent", agent, "stream", stream, "shown", first, "redundant", second)
}

// warnUnresolved logs, once per destination, that a binding's destination names no
// conversation, so its output goes nowhere.
func (l *Layer) warnUnresolved(d Dest, why string) {
	if _, seen := l.unshown.LoadOrStore("dest\x00"+d.String(), true); seen {
		return
	}
	slog.Warn("binding sends nothing: "+why, "destination", d.String())
}
