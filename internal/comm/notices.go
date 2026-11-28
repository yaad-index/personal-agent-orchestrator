package comm

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// NotifyAgent sends pao's own message about one agent, such as a gate question
// the agent's call raised or a notice that it did not start, to the agent's
// stderr destinations (ADR-0029 point 4). Where the agent has no stderr binding,
// it goes to the permission channel of the user the agent belongs to
// (ADR-0058 point 6), and for an agent of no user, to the private conversation
// of each operator bound on a channel, and every channel with one
// conversation. The governor's go to the subject it is serving instead
// (governorStderr). Like Notify, the message is stored in no agent's scope.
//
// A stderr destination is shown the message only if every one of its readers may
// see it (point 6). One that may not is held back and logged once, never
// replaced by another. Where the agent's stderr bindings leave no conversation at
// all, nothing is sent and an error says so: a question nobody is shown cannot
// be answered.
func (l *Layer) NotifyAgent(ctx context.Context, agent string, m core.Message) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	targets, err := l.stderrTargets(agent, m)
	if err != nil {
		return core.Message{}, err
	}
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets})
}

// stderrTargets returns where pao's message m about agent goes.
func (l *Layer) stderrTargets(agent string, m core.Message) ([]target, error) {
	if l.governor != "" && agent == l.governor {
		return l.governorStderr(agent, m)
	}
	bs := l.bindingsOf(agent, StreamStderr)
	if len(bs) == 0 {
		if user := l.userOf(agent); user != "" && l.identity != nil {
			return l.permissionTargets(agent, user, m)
		}
		return l.operatorsPrivately(agent), nil
	}
	var out []target
	first := map[origin]string{}
	for _, b := range bs {
		for _, at := range l.resolve(b.To) {
			if d, seen := first[at]; seen {
				l.warnSameConversation(agent, StreamStderr, d, b.To.String())
				continue
			}
			first[at] = b.To.String()
			if !l.noticeMay(at, m) {
				if _, warned := l.unshown.LoadOrStore("stderr\x00"+agent+"\x00"+b.To.String(), true); !warned {
					slog.Warn("stderr destination held back: a reader of it may not see pao's messages about the agent", "agent", agent, "destination", b.To.String())
				}
				continue
			}
			out = append(out, target{kind: toStderr, at: at, dest: b.To.String(), agent: agent})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("agent %q: no stderr destination shows pao's message", agent)
	}
	return out, nil
}

// operatorsPrivately is the private conversation of each operator bound on a
// channel, for pao's message about agent. A chat channel shows it only to its
// owner, privately, so of an owner's channels on one provider, which share that
// one conversation, the first is enough.
func (l *Layer) operatorsPrivately(agent string) []target {
	var out []target
	reached := map[string]bool{}
	for _, ch := range l.Channels() {
		key := ch
		if s, ok := l.scopes[ch]; ok {
			key = l.providerOf(ch) + "\x00" + s.Owner
		}
		if reached[key] {
			continue
		}
		reached[key] = true
		out = append(out, target{kind: toPrivate, at: origin{channel: ch}, agent: agent})
	}
	return out
}

// userOf is the user agent belongs to, by its profile; "" for none.
func (l *Layer) userOf(agent string) string {
	ps, err := l.router.Resolve(core.Target{Kind: core.ByName, Value: agent})
	if err != nil || len(ps) != 1 || ps[0].Name != agent {
		return ""
	}
	return ps[0].User
}

// permissionTargets is where pao's message m about agent, of user, goes: the
// user's permission channel (ADR-0058 point 6). That is the channel the user
// set, if it is still one of theirs, or otherwise the user's private
// conversation, reached once through each provider that carries a channel of
// theirs. A user who is not an operator may not answer a gate question
// (ADR-0046 point 4), so the operators are told as well.
func (l *Layer) permissionTargets(agent, user string, m core.Message) ([]target, error) {
	var out []target
	ch := l.identity.Permission(user)
	if ch != "" {
		s, ok := l.scopes[ch]
		switch {
		case !ok || s.Owner != user || !l.HasChannel(ch):
			if _, seen := l.unshown.LoadOrStore("permission\x00"+user+"\x00"+ch, true); !seen {
				slog.Warn("a user's permission channel is not a channel of theirs; their private conversation is used", "user", user, "channel", ch)
			}
			ch = ""
		case s.Conversation == "":
			out = append(out, target{kind: toPrivate, at: origin{channel: ch}, agent: agent})
		default:
			at := origin{channel: ch, conversation: s.Conversation}
			if l.noticeMay(at, m) {
				out = append(out, target{kind: toStderr, at: at, dest: ch, agent: agent})
			}
		}
	}
	if ch == "" {
		names := make([]string, 0, len(l.scopes))
		for name, s := range l.scopes {
			if s.Owner == user && l.HasChannel(name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		reached := map[string]bool{}
		for _, name := range names {
			if p := l.providerOf(name); !reached[p] {
				reached[p] = true
				out = append(out, target{kind: toPrivate, at: origin{channel: name}, agent: agent})
			}
		}
	}
	if !l.identity.IsOperator(user) {
		out = append(out, l.operatorsPrivately(agent)...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("agent %q: its user %s has no permission channel pao can show a message in", agent, user)
	}
	return out, nil
}

// governorStderr is where pao's message about the governor goes: to the subject
// it is serving, alone and privately, on the channel the request came from, as
// its output does (ADR-0032 point 4). Never to every operator. With no request in
// hand there is no one to tell: nothing is sent, and the error says so, so a
// question that cannot be put is refused.
func (l *Layer) governorStderr(agent string, m core.Message) ([]target, error) {
	l.mu.RLock()
	t, ok := l.origins[agent]
	l.mu.RUnlock()
	if !ok || t.requester == "" {
		return nil, fmt.Errorf("agent %q is the governor and is serving no request, so there is no one to tell", agent)
	}
	targets := l.toSubject(t.at.channel, t.at.conversation == "", t.requester, agent, m)
	if len(targets) == 0 {
		return nil, fmt.Errorf("agent %q: %s has no private conversation an operator reads on %s", agent, t.requester, t.at.channel)
	}
	return targets, nil
}

// noticeMay reports whether every reader of a conversation may see pao's message
// m. A channel's one conversation, as on the local channel, is the node
// operator's own.
func (l *Layer) noticeMay(at origin, m core.Message) bool {
	return at.conversation == "" || l.originMay(at, m)
}

// NotifyOperators sends pao's own message about the node, not about one agent,
// such as an account whose budget cannot be read, to the private conversation
// of each operator bound on a channel, as an agent's notice goes where it has no
// stderr binding (ADR-0029 point 4). Never to a group, and never to every
// conversation as Notify does. Like Notify, it is stored in no agent's scope.
func (l *Layer) NotifyOperators(ctx context.Context, m core.Message) (core.Message, error) {
	return l.notifyOperators(ctx, m, l.Channels())
}

// NotifyOperatorsOn is NotifyOperators on the one channel named: each operator's
// private conversation there, never a group on it. A channel that is not
// registered is an error, never a fall back to every channel.
func (l *Layer) NotifyOperatorsOn(ctx context.Context, channel string, m core.Message) (core.Message, error) {
	if !l.HasChannel(channel) {
		return core.Message{}, fmt.Errorf("no channel %q", channel)
	}
	return l.notifyOperators(ctx, m, []string{channel})
}

func (l *Layer) notifyOperators(ctx context.Context, m core.Message, channels []string) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	var targets []target
	for _, ch := range channels {
		targets = append(targets, target{kind: toPrivate, at: origin{channel: ch}})
	}
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	return stored, l.sendRouted(ctx, stored, outRoute{targets: targets})
}
