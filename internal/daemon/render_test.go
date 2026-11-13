package daemon

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestRender(t *testing.T) {
	levels := core.DefaultLevels()
	cases := []struct {
		name  string
		e     core.Entry
		level int
		want  string
		shown bool
	}{
		{"assistant message", core.Entry{Kind: core.KindMessage, Role: "assistant", Text: "hi"}, 1, "hi", true},
		{"human side is never echoed", core.Entry{Kind: core.KindMessage, Role: "user", Text: "[h]: hi"}, 4, "", false},
		{"sidechain left out", core.Entry{Kind: core.KindMessage, Role: "assistant", Text: "inner", Sidechain: true}, 4, "", false},
		{"tool call hidden at level 1", core.Entry{Kind: core.KindToolCall, Tool: "Bash", Text: `{"c":"ls"}`}, 1, "", false},
		{"tool call at level 2", core.Entry{Kind: core.KindToolCall, Tool: "Bash", Text: `{"c":"ls"}`}, 2, `tool Bash: {"c":"ls"}`, true},
		{"tool result at level 3", core.Entry{Kind: core.KindToolResult, Text: "out"}, 3, "result: out", true},
		{"tool error", core.Entry{Kind: core.KindToolResult, Text: "denied", IsError: true}, 3, "error: denied", true},
		{"reasoning at level 3", core.Entry{Kind: core.KindReasoning, Text: "hm"}, 3, "thinking: hm", true},
		{"reasoning hidden at level 2", core.Entry{Kind: core.KindReasoning, Text: "hm"}, 2, "", false},
		{"other at level 4", core.Entry{Kind: core.KindOther, Type: "system/notice", Text: "x"}, 4, "[system/notice] x", true},
		{"empty text is not shown", core.Entry{Kind: core.KindMessage, Role: "assistant", Text: "  \n"}, 1, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, shown := render(tc.e, levels, tc.level)
			assert.Equal(t, tc.shown, shown)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderTruncatesALongEntry(t *testing.T) {
	got, shown := render(core.Entry{Kind: core.KindMessage, Role: "assistant", Text: strings.Repeat("é", maxRendered+50)}, core.DefaultLevels(), 1)
	assert.True(t, shown)
	assert.Equal(t, maxRendered+1, len([]rune(got)), "cut at the rune limit plus the ellipsis, never mid-rune")
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestRenderHonoursAProfileLevelOverride(t *testing.T) {
	p := core.Profile{Levels: core.Levels{core.KindReasoning: 1}}
	_, shown := render(core.Entry{Kind: core.KindReasoning, Text: "hm"}, p.EffectiveLevels(), 1)
	assert.True(t, shown, "a profile can promote reasoning to the lowest level")
}
