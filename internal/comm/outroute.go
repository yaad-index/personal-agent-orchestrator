package comm

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// The kinds of target an agent's message is sent to.
const (
	// toOrigin is the conversation the agent's turn began in.
	toOrigin = iota
	// toWide is every conversation on a channel that may show the message.
	toWide
	// toBinding is a conversation a standing binding names.
	toBinding
	// toMirror is a conversation a binding at the log level names, shown a
	// message delivered to the agent.
	toMirror
	// toPrivate is the private conversation of each operator bound on a
	// channel: where pao's messages about an agent go when it has no stderr
	// binding.
	toPrivate
	// toStderr is a conversation a stderr binding names.
	toStderr
	// toSubject is a private conversation of one subject: the one a proposal
	// is for, or the sender of a command a later answer is for.
	toSubject
)

// target is one place an agent's message goes.
type target struct {
	kind int
	at   origin
	// dest names a binding's destination, for the log.
	dest string
	// agent is the agent a mirrored message was delivered to, or a message of
	// pao's is about.
	agent string
}

// outRoute is where one stored message of an agent's is going, held while the
// channels are handed it so each can tell its conversations apart.
type outRoute struct {
	targets []target
	// covered are the conversations a standing binding of the agent's shows the
	// output in. A wide target leaves them out: they get the binding's copy.
	covered map[origin]bool
}

// rendering is one way an agent's output is shown: at a level, with a prefix.
type rendering struct {
	level  int
	prefix string
}

// plan is where an agent's output goes now, per rendering.
type plan struct {
	// def is the rendering at the agent's own level, with no prefix.
	def rendering
	// byRendering are the targets of each rendering, the default first.
	byRendering []planned
	covered     map[origin]bool
}

type planned struct {
	r rendering
	// targets are the rendering's targets; base are those of the default
	// stream, the rest are standing bindings'.
	targets []target
	base    int
}

// planFor works out where the agent's output goes, at its own level def:
//
//   - In a turn with an origin, to the origin's conversation, unless a standing
//     binding already shows the output there (ADR-0029 point 2).
//   - Where the agent's output is bound to a channel as a whole, to every
//     conversation on that channel that may show it.
//   - In a turn with no origin and with no channel bound, to every channel. This is
//     interim, until output with no origin goes only to standing bindings.
//   - To every standing binding's conversations, at the binding's level and with
//     its prefix (points 3 and 5).
//
// A conversation reached two ways gets one copy, the binding's.
func (l *Layer) planFor(agent string, def int) plan {
	l.mu.RLock()
	only, bound := l.bound[agent]
	t, hasOrigin := l.origins[agent]
	l.mu.RUnlock()
	o := t.at

	p := plan{def: rendering{level: def}, covered: map[origin]bool{}}
	byR := map[rendering][]target{}
	var order []rendering
	add := func(r rendering, t target) {
		if _, ok := byR[r]; !ok && r != p.def {
			order = append(order, r)
		}
		byR[r] = append(byR[r], t)
	}
	first := map[origin]string{}
	for _, b := range l.bindingsOf(agent, StreamStdout) {
		for _, at := range l.resolve(b.To) {
			if p.covered[at] {
				l.warnSameConversation(agent, StreamStdout, first[at], b.To.String())
				continue
			}
			p.covered[at] = true
			first[at] = b.To.String()
			add(rendering{level: b.Level, prefix: b.Prefix}, target{kind: toBinding, at: at, dest: b.To.String()})
		}
	}

	var base []target
	wide := func(ch string) {
		// A channel with one conversation that a binding shows the output in
		// gets the binding's copy only.
		if !p.covered[origin{channel: ch}] {
			base = append(base, target{kind: toWide, at: origin{channel: ch}})
		}
	}
	switch {
	case hasOrigin && !p.covered[o]:
		base = append(base, target{kind: toOrigin, at: o})
	case !hasOrigin && !bound:
		for _, ch := range l.Channels() {
			wide(ch)
		}
	}
	if bound {
		wide(only)
	}

	p.byRendering = append(p.byRendering, planned{r: p.def, targets: append(base, byR[p.def]...), base: len(base)})
	for _, r := range order {
		p.byRendering = append(p.byRendering, planned{r: r, targets: byR[r]})
	}
	return p
}

// SendEntry shows one entry of an agent's output wherever it goes (planFor).
// render returns the entry rendered at a level, or false where the level shows
// nothing of it; it is asked once per rendering needed. hold, when set, may hold
// back the default stream's copy for a paused viewer; it is let go later through
// Send. Each rendering is stored once in the agent's scope.
func (l *Layer) SendEntry(ctx context.Context, agent string, def int, render func(level int) (core.Message, bool), hold func(core.Message) bool) error {
	p := l.planFor(agent, def)
	var errs []error
	for i, pl := range p.byRendering {
		m, ok := render(pl.r.level)
		if !ok {
			continue
		}
		targets := pl.targets
		if i == 0 && pl.base > 0 && hold != nil && hold(m) {
			targets = targets[pl.base:]
		}
		if len(targets) == 0 {
			continue
		}
		if _, err := l.sendRendered(ctx, agent, m, pl.r.prefix, targets, p.covered); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sendRendered stores m in the agent's scope and hands it to the channels of its
// targets.
func (l *Layer) sendRendered(ctx context.Context, agent string, m core.Message, prefix string, targets []target, covered map[origin]bool) (core.Message, error) {
	stored, err := l.store.Add(m, agent)
	if err != nil {
		return core.Message{}, err
	}
	stored.Label = prefix
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets, covered: covered})
}

// sendRouted hands a stored message to the channels of its targets, holding its
// route while they ask where it goes.
func (l *Layer) sendRouted(ctx context.Context, stored core.Message, r outRoute) error {
	l.routes.Store(stored.ID, r)
	defer l.routes.Delete(stored.ID)
	var channels []string
	for _, t := range r.targets {
		if !slices.Contains(channels, t.at.channel) {
			channels = append(channels, t.at.channel)
		}
	}
	var errs []error
	for _, ch := range channels {
		if err := l.sendTo(ctx, ch, stored); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// routed returns the conversations on channel that m, sent on route r, goes to.
// Each conversation is checked against everyone who reads it (ADR-0029 point 6).
// A reply that fails there is held back, never sent somewhere else.
func (l *Layer) routed(channel string, m core.Message, r outRoute) []string {
	var out []string
	add := func(c string) {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, t := range r.targets {
		if t.at.channel != channel {
			continue
		}
		switch t.kind {
		case toWide:
			for _, c := range l.wide(channel, m) {
				if !r.covered[origin{channel: channel, conversation: c}] {
					add(c)
				}
			}
		case toOrigin:
			if l.originMay(t.at, m) {
				add(t.at.conversation)
				continue
			}
			slog.Warn("reply held back: a reader of the conversation it was addressed from may not see it",
				"agent", m.Sender, "channel", channel, "conversation", l.conversationName(t.at), "message", l.Qualify(m.ID))
		case toBinding:
			if l.originMay(t.at, m) {
				add(t.at.conversation)
				continue
			}
			if _, seen := l.unshown.LoadOrStore("held\x00"+m.Sender+"\x00"+t.dest, true); !seen {
				slog.Warn("binding held back: a reader of its destination may not see the agent's output", "agent", m.Sender, "destination", t.dest)
			}
		case toMirror:
			if l.mirrorMay(t.at, t.agent, m) {
				add(t.at.conversation)
				continue
			}
			if _, seen := l.unshown.LoadOrStore("mirror\x00"+t.agent+"\x00"+t.dest, true); !seen {
				slog.Warn("mirrored message held back: a reader of the destination may not see it", "agent", t.agent, "destination", t.dest)
			}
		case toPrivate:
			for _, c := range l.privately(channel, m) {
				add(c)
			}
		case toStderr, toSubject:
			if l.noticeMay(t.at, m) {
				add(t.at.conversation)
			}
		}
	}
	return out
}
