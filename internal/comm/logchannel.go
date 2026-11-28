package comm

import (
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// SetLogChannels gives the layer the agents' log channels (ADR-0058 point 7):
// logs returns them by channel name, each with the agent it is the log channel
// of, read afresh, as a profile may change while the node runs. A log channel is
// shown all of its agent's output, at the log level, and takes no input. Call it
// before the layer starts receiving.
func (l *Layer) SetLogChannels(logs func() map[string]string) { l.logs = logs }

func (l *Layer) logChannels() map[string]string {
	if l.logs == nil {
		return nil
	}
	return l.logs()
}

// isLog reports whether channel is an agent's log channel.
func (l *Layer) isLog(channel string) bool {
	_, ok := l.logChannels()[channel]
	return ok
}

// logOf returns agent's log channel.
func (l *Layer) logOf(agent string) (string, bool) {
	for ch, a := range l.logChannels() {
		if a == agent {
			return ch, true
		}
	}
	return "", false
}

// logBinding is the binding an agent's log channel is: all of its output, and
// what is delivered to it, at the log level. A paused log channel has none.
func (l *Layer) logBinding(agent string) (Binding, bool) {
	ch, ok := l.logOf(agent)
	if !ok {
		return Binding{}, false
	}
	if _, paused := l.logPaused.Load(agent); paused {
		return Binding{}, false
	}
	return Binding{Agent: agent, Stream: StreamStdout, To: Dest{Kind: DestChannel, Name: ch}, Level: core.LevelLog}, true
}

// PauseLog pauses the agent's log channel, or resumes it (ADR-0058 point 7).
// What the agent says while it is paused is not shown there later; it stays in
// the agent's transcript. A pause lasts until it is resumed or pao starts again.
func (l *Layer) PauseLog(agent string, pause bool) error {
	if _, ok := l.logOf(agent); !ok {
		return fmt.Errorf("agent %q has no log channel", agent)
	}
	if pause {
		l.logPaused.Store(agent, true)
	} else {
		l.logPaused.Delete(agent)
	}
	return nil
}
