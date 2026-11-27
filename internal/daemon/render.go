package daemon

import (
	"fmt"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// maxRendered caps one rendered entry; a long tool result is cut, not dropped.
const maxRendered = 2000

// render turns a transcript entry into the text the human sees, or reports
// that the entry is not shown. Only the agent's own words are conversation: the
// human-side messages in the transcript are pao's own pastes (or the operator
// typing in the terminal) and are never echoed back, and a sub-agent's inner
// work is left out. Everything else is shown or hidden by the profile's level.
func render(e core.Entry, levels core.Levels, level int) (string, bool) {
	if e.Sidechain {
		return "", false
	}
	if e.Kind == core.KindMessage && e.Role != "assistant" {
		return "", false
	}
	if !levels.Visible(e.Kind, level) {
		return "", false
	}

	var text string
	switch e.Kind {
	case core.KindMessage:
		text = e.Text
	case core.KindToolCall:
		text = fmt.Sprintf("tool %s: %s", e.Tool, e.Text)
	case core.KindToolResult:
		prefix := "result"
		if e.IsError {
			prefix = "error"
		}
		text = prefix + ": " + e.Text
	case core.KindReasoning:
		text = "thinking: " + e.Text
	default:
		text = "[" + e.Type + "] " + e.Text
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return truncate(text, maxRendered), true
}

// entryMessage is the message an agent's transcript entry is shown as at level,
// or false where the entry is not shown at that level.
func entryMessage(agent string, e core.Entry, levels core.Levels, level int) (core.Message, bool) {
	text, ok := render(e, levels, level)
	if !ok {
		return core.Message{}, false
	}
	return core.Message{Sender: agent, Kind: core.MessageContent, Text: text, Format: formatOf(e), Time: e.Time}, true
}

// formatOf is how a rendered entry's text is written: the agent's own words are
// Markdown, and what pao renders from the rest of the transcript (tool calls,
// results, reasoning) is plain, never parsed (ADR-0050 point 2).
func formatOf(e core.Entry) core.Format {
	if e.Kind == core.KindMessage {
		return core.FormatMarkdown
	}
	return core.FormatPlain
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
