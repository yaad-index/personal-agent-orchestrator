package claudecode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func parse(t *testing.T, line string) []core.Entry {
	t.Helper()
	got, err := Reader{}.Parse([]byte(line))
	require.NoError(t, err)
	return got
}

func TestAssistantText(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u1","timestamp":"2026-11-08T19:41:00.428Z","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`)
	require.Len(t, got, 1)
	assert.Equal(t, core.KindMessage, got[0].Kind)
	assert.Equal(t, "assistant", got[0].Role)
	assert.Equal(t, "hello", got[0].Text)
	assert.Equal(t, "u1", got[0].ID)
	assert.Equal(t, time.Date(2026, 11, 8, 19, 41, 0, 428000000, time.UTC), got[0].Time)
}

func TestUserStringContent(t *testing.T) {
	got := parse(t, `{"type":"user","uuid":"u2","message":{"role":"user","content":"do the thing"}}`)
	require.Len(t, got, 1)
	assert.Equal(t, core.KindMessage, got[0].Kind)
	assert.Equal(t, "user", got[0].Role)
	assert.Equal(t, "do the thing", got[0].Text)
}

func TestInjectedUserTextIsNotConversation(t *testing.T) {
	got := parse(t, `{"type":"user","uuid":"u3","isMeta":true,"message":{"role":"user","content":"<reminder>x</reminder>"}}`)
	require.Len(t, got, 1)
	assert.Equal(t, core.KindOther, got[0].Kind)
	assert.Equal(t, "user", got[0].Type)
}

func TestReasoning(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u4","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me think","signature":"sig"}]}}`)
	require.Len(t, got, 1)
	assert.Equal(t, core.KindReasoning, got[0].Kind)
	assert.Equal(t, "let me think", got[0].Text)
}

func TestRedactedReasoningIsSkipped(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u5","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`)
	assert.Empty(t, got)
}

func TestToolCall(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u6","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`)
	require.Len(t, got, 1)
	assert.Equal(t, core.KindToolCall, got[0].Kind)
	assert.Equal(t, "Bash", got[0].Tool)
	assert.Equal(t, "toolu_1", got[0].ToolID)
	assert.JSONEq(t, `{"command":"ls"}`, got[0].Text)
}

func TestToolResultShapes(t *testing.T) {
	str := parse(t, `{"type":"user","uuid":"u7","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file.txt"}]}}`)
	require.Len(t, str, 1)
	assert.Equal(t, core.KindToolResult, str[0].Kind)
	assert.Equal(t, "toolu_1", str[0].ToolID)
	assert.Equal(t, "file.txt", str[0].Text)
	assert.False(t, str[0].IsError)

	list := parse(t, `{"type":"user","uuid":"u8","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}}`)
	require.Len(t, list, 1)
	assert.Equal(t, "a\nb", list[0].Text)

	failed := parse(t, `{"type":"user","uuid":"u9","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"denied","is_error":true}]}}`)
	require.Len(t, failed, 1)
	assert.True(t, failed[0].IsError)
}

func TestOneRecordCanYieldSeveralEntriesInOrder(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u10","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"ok"},{"type":"tool_use","id":"t","name":"Read","input":{}}]}}`)
	require.Len(t, got, 3)
	assert.Equal(t, []core.Kind{core.KindReasoning, core.KindMessage, core.KindToolCall},
		[]core.Kind{got[0].Kind, got[1].Kind, got[2].Kind})
	for _, e := range got {
		assert.Equal(t, "u10", e.ID)
	}
}

func TestSidechainIsMarked(t *testing.T) {
	got := parse(t, `{"type":"assistant","uuid":"u11","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"sub"}]}}`)
	require.Len(t, got, 1)
	assert.True(t, got[0].Sidechain)
}

func TestOtherRecordsAreKept(t *testing.T) {
	sys := parse(t, `{"type":"system","subtype":"turn_duration","uuid":"u12","durationMs":5}`)
	require.Len(t, sys, 1)
	assert.Equal(t, core.KindOther, sys[0].Kind)
	assert.Equal(t, "system/turn_duration", sys[0].Type)

	noted := parse(t, `{"type":"system","subtype":"notice","uuid":"u13","content":"compacted"}`)
	assert.Equal(t, "compacted", noted[0].Text)

	unknown := parse(t, `{"type":"brand-new-thing","uuid":"u14"}`)
	require.Len(t, unknown, 1)
	assert.Equal(t, core.KindOther, unknown[0].Kind)
	assert.Equal(t, "brand-new-thing", unknown[0].Type)

	unknownBlock := parse(t, `{"type":"assistant","uuid":"u15","message":{"role":"assistant","content":[{"type":"hologram"}]}}`)
	require.Len(t, unknownBlock, 1)
	assert.Equal(t, core.KindOther, unknownBlock[0].Kind)
	assert.Equal(t, "block:hologram", unknownBlock[0].Type)
}

func TestMalformedLines(t *testing.T) {
	for name, line := range map[string]string{
		"not json":          `{"type":`,
		"content not valid": `{"type":"assistant","message":{"role":"assistant","content":42}}`,
		"missing content":   `{"type":"assistant","message":{"role":"assistant"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Reader{}.Parse([]byte(line))
			assert.Error(t, err)
		})
	}
}
