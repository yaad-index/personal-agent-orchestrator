package comm

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// NotifyAgent sends pao's own message about one agent, such as a gate question
// the agent's call raised or a notice that it did not start, to the agent's
// stderr destinations (ADR-0029 point 4). Where the agent has no stderr binding,
// those are the private conversation of each operator bound on a channel, and
// every channel with one conversation. The governor's go to the subject it is
// serving instead (governorStderr). Like Notify, the message is stored in no
// agent's scope.
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
		var out []target
		for _, ch := range l.Channels() {
			out = append(out, target{kind: toPrivate, at: origin{channel: ch}, agent: agent})
		}
		return out, nil
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
