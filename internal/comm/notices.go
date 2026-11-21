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
// every channel with one conversation. Like Notify, the message is stored in no
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
	bs := l.bindingsOf(agent, StreamStderr)
	if len(bs) == 0 {
		var out []target
		for _, ch := range l.Channels() {
			out = append(out, target{kind: toPrivate, at: origin{channel: ch}, agent: agent})
		}
		return out, nil
	}
	var out []target
	for _, b := range bs {
		for _, at := range l.resolve(b.To) {
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

// noticeMay reports whether every reader of a conversation may see pao's message
// m. A channel's one conversation, as on the local channel, is the node
// operator's own.
func (l *Layer) noticeMay(at origin, m core.Message) bool {
	return at.conversation == "" || l.originMay(at, m)
}
