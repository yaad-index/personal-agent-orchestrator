// Package claudecode reads the session transcript Claude Code writes: one JSON
// record per line. It is the per-backend transcript reader of ADR-0011; the
// format it understands is pinned by the contract test of ADR-0008.
package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Format is the name a profile uses to select this reader.
const Format = "claude-code"

// Reader implements core.TranscriptReader.
type Reader struct{}

var _ core.TranscriptReader = Reader{}

type record struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	Timestamp   string          `json:"timestamp"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Subtype     string          `json:"subtype"`
	Content     json.RawMessage `json:"content"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Parse converts one transcript line into entries. Malformed JSON is an error.
// A record type the reader does not know becomes a KindOther entry, so a new
// record type shows up in a verbose view instead of vanishing.
func (Reader) Parse(line []byte) ([]core.Entry, error) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, fmt.Errorf("transcript line: %w", err)
	}
	base := core.Entry{ID: rec.UUID, Sidechain: rec.IsSidechain, Time: parseTime(rec.Timestamp)}

	if rec.Message == nil || (rec.Type != "user" && rec.Type != "assistant") {
		return []core.Entry{other(base, rec)}, nil
	}
	if rec.IsMeta {
		// Text the harness injected on the human side (reminders and the like)
		// is not conversation.
		return []core.Entry{other(base, rec)}, nil
	}

	role := rec.Message.Role
	if role == "" {
		role = rec.Type
	}

	// Content is either a plain string or a list of typed blocks.
	var text string
	if err := json.Unmarshal(rec.Message.Content, &text); err == nil {
		e := base
		e.Kind, e.Role, e.Text = core.KindMessage, role, text
		return []core.Entry{e}, nil
	}

	var blocks []block
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		return nil, fmt.Errorf("transcript line: message content: %w", err)
	}
	var out []core.Entry
	for _, b := range blocks {
		e := base
		switch b.Type {
		case "text":
			e.Kind, e.Role, e.Text = core.KindMessage, role, b.Text
		case "thinking":
			if b.Thinking == "" {
				continue // redacted: nothing readable to show
			}
			e.Kind, e.Text = core.KindReasoning, b.Thinking
		case "tool_use":
			e.Kind, e.Tool, e.ToolID, e.Text = core.KindToolCall, b.Name, b.ID, compact(b.Input)
		case "tool_result":
			e.Kind, e.ToolID, e.IsError, e.Text = core.KindToolResult, b.ToolUseID, b.IsError, resultText(b.Content)
		default:
			e.Kind, e.Type = core.KindOther, "block:"+b.Type
		}
		out = append(out, e)
	}
	return out, nil
}

func other(base core.Entry, rec record) core.Entry {
	e := base
	e.Kind = core.KindOther
	e.Type = rec.Type
	if rec.Subtype != "" {
		e.Type += "/" + rec.Subtype
	}
	// A string is shown as is; structured content keeps its JSON, so an
	// unfamiliar record's payload is still visible in a verbose view.
	e.Text = resultText(rec.Content)
	return e
}

// resultText flattens a tool result: a string, or a list of text blocks.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return compact(raw)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
