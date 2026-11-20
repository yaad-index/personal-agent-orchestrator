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

// Binding is a standing binding: Dest follows the agent's output at Level
// (ADR-0029 point 3). A binding carries all of the agent's output, whichever
// conversation a turn began in. Prefix, when set, is shown before each message in
// place of the agent's name (point 5).
type Binding struct {
	Agent  string
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

// bindingsOf returns the agent's standing bindings.
func (l *Layer) bindingsOf(agent string) []Binding {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Binding
	for _, b := range l.bindings {
		if b.Agent == agent {
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
			senders := l.identity.Senders(d.Name, ch)
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
			return []origin{{channel: d.Name}}
		}
	}
	l.warnUnresolved(d, "it names no conversation")
	return nil
}

// warnUnresolved logs, once per destination, that a binding's destination names no
// conversation, so its output goes nowhere.
func (l *Layer) warnUnresolved(d Dest, why string) {
	if _, seen := l.unshown.LoadOrStore("dest\x00"+d.String(), true); seen {
		return
	}
	slog.Warn("binding sends nothing: "+why, "destination", d.String())
}
