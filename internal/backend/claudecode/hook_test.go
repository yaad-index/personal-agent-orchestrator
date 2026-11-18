package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHookInput(t *testing.T) {
	in, err := ParseHookInput(strings.NewReader(
		`{"session_id":"s","cwd":"/w","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	require.NoError(t, err)
	assert.Equal(t, "Bash", in.Tool)
	assert.JSONEq(t, `{"command":"ls"}`, string(in.Input))
}

func TestParseHookInputWithoutAnEventName(t *testing.T) {
	in, err := ParseHookInput(strings.NewReader(`{"tool_name":"Read","tool_input":{}}`))
	require.NoError(t, err)
	assert.Equal(t, "Read", in.Tool)
}

func TestParseHookInputRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":    `{`,
		"no tool":     `{"hook_event_name":"PreToolUse","tool_input":{}}`,
		"wrong event": `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{}}`,
		"empty":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHookInput(strings.NewReader(body))
			assert.Error(t, err)
		})
	}
}

func TestHookOutput(t *testing.T) {
	for _, tc := range []struct {
		allow bool
		want  string
	}{{true, "allow"}, {false, "deny"}} {
		var got struct {
			Out struct {
				Event    string `json:"hookEventName"`
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal(HookOutput(tc.allow, "because"), &got))
		assert.Equal(t, "PreToolUse", got.Out.Event)
		assert.Equal(t, tc.want, got.Out.Decision)
		assert.Equal(t, "because", got.Out.Reason)
	}
}
