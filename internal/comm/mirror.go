package comm

import (
	"context"
	"log/slog"
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// mirrorInput shows m, about to be delivered to agents, in each destination that
// follows one of them at the log level, which mirrors the agent's whole stream,
// including what it is sent (ADR-0029 point 3). It is shown before it is delivered,
// so the agent's answer never comes first; a delivery that then fails was still
// shown. The conversation m came from is not shown it again. A destination
// following several of the agents is shown it once.
func (l *Layer) mirrorInput(ctx context.Context, in Inbound, m core.Message, agents []core.Profile) {
	from, fromChannel := origin{channel: in.Source, conversation: in.Conversation}, l.HasChannel(in.Source)
	seen := map[origin]bool{}
	var targets []target
	for _, a := range agents {
		for _, b := range l.bindingsOf(a.Name, StreamStdout) {
			if b.Level < core.LevelLog {
				continue
			}
			for _, at := range l.resolve(b.To) {
				if seen[at] || (fromChannel && at == from) {
					continue
				}
				seen[at] = true
				targets = append(targets, target{kind: toMirror, at: at, dest: b.To.String(), agent: a.Name})
			}
		}
	}
	if len(targets) == 0 {
		return
	}
	l.routes.Store(m.ID, outRoute{targets: targets})
	defer l.routes.Delete(m.ID)
	var channels []string
	for _, t := range targets {
		if !slices.Contains(channels, t.at.channel) {
			channels = append(channels, t.at.channel)
			if err := l.sendTo(ctx, t.at.channel, m); err != nil {
				slog.Warn("could not mirror a delivered message", "channel", t.at.channel, "message", l.Qualify(m.ID), "error", err)
			}
		}
	}
}

// mirrorMay reports whether every reader of a conversation may see m, a message
// delivered to agent: they may reach the agent, and, where m comes from another
// agent, that agent too (ADR-0029 point 6).
func (l *Layer) mirrorMay(at origin, agent string, m core.Message) bool {
	if !l.originMay(at, core.Message{Sender: agent, Kind: core.MessageContent}) {
		return false
	}
	if _, err := l.router.Resolve(core.Target{Kind: core.ByName, Value: m.Sender}); err == nil {
		return l.originMay(at, m)
	}
	return true
}
