package core

import (
	"fmt"
	"time"
)

// Kind classifies a transcript entry (ADR-0008 point 2).
type Kind string

const (
	// KindMessage is conversational text, from the human side or the agent.
	KindMessage Kind = "message"
	// KindReasoning is the agent's visible reasoning.
	KindReasoning Kind = "reasoning"
	// KindToolCall is the agent invoking a tool.
	KindToolCall Kind = "tool_call"
	// KindToolResult is a tool's result returned to the agent.
	KindToolResult Kind = "tool_result"
	// KindOther is everything else the backend writes (bookkeeping, system
	// notices). It is kept, not dropped, so a verbose view can show it.
	KindOther Kind = "other"
)

// Kinds lists every kind, in the order a viewer meets them as detail grows.
var Kinds = []Kind{KindMessage, KindToolCall, KindToolResult, KindReasoning, KindOther}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

// Entry is one normalized item read from a backend's transcript.
type Entry struct {
	// ID is the backend's identifier for the transcript record the entry came
	// from. Several entries from one record share it.
	ID   string
	Time time.Time
	Kind Kind
	// Role is "user" or "assistant" for a message; empty for other kinds.
	Role string
	// Text is the entry's readable content.
	Text string
	// Tool names the tool of a tool call.
	Tool string
	// ToolID links a tool call to its result.
	ToolID string
	// IsError marks a tool result that reports failure.
	IsError bool
	// Sidechain marks work done by a sub-agent inside the main agent.
	Sidechain bool
	// Type is the backend's own record type, kept for KindOther entries.
	Type string
}

// TranscriptReader turns one line of a backend's transcript into entries. It is
// the small per-backend part ADR-0011 allows: everything else about mirroring
// is backend-neutral. A line may yield several entries or none.
type TranscriptReader interface {
	Parse(line []byte) ([]Entry, error)
}

// Levels maps each kind to the lowest verbosity level at which it is shown
// (ADR-0008 point 2). Viewing at level N shows every kind whose level is at
// most N.
type Levels map[Kind]int

// DefaultLevels is the level map a profile gets when it declares none:
// conversation first, then tool calls, then results and reasoning, then the
// rest.
func DefaultLevels() Levels {
	return Levels{
		KindMessage:    1,
		KindToolCall:   2,
		KindToolResult: 3,
		KindReasoning:  3,
		KindOther:      4,
	}
}

// Visible reports whether an entry of kind k shows at level n. A kind the map
// does not mention is not shown at any level, so a new kind never appears
// unannounced.
func (l Levels) Visible(k Kind, n int) bool {
	lvl, ok := l[k]
	return ok && lvl <= n
}

// Validate checks every level is at least 1 and every key is a known kind.
func (l Levels) Validate() error {
	for k, lvl := range l {
		if !k.Valid() {
			return fmt.Errorf("unknown kind %q", k)
		}
		if lvl < 1 {
			return fmt.Errorf("kind %q: level must be at least 1", k)
		}
	}
	return nil
}
