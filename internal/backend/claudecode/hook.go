package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// HookInput is what Claude Code's pre-tool-call hook (PreToolUse) is given on
// standard input: the tool about to run and its arguments.
type HookInput struct {
	Tool  string
	Input json.RawMessage
}

// ParseHookInput reads a PreToolUse hook payload.
func ParseHookInput(r io.Reader) (HookInput, error) {
	var in struct {
		Event string          `json:"hook_event_name"`
		Tool  string          `json:"tool_name"`
		Input json.RawMessage `json:"tool_input"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&in); err != nil {
		return HookInput{}, fmt.Errorf("hook input: %w", err)
	}
	if in.Event != "" && in.Event != "PreToolUse" {
		return HookInput{}, fmt.Errorf("hook input: unexpected event %q", in.Event)
	}
	if in.Tool == "" {
		return HookInput{}, errors.New("hook input: no tool_name")
	}
	return HookInput{Tool: in.Tool, Input: in.Input}, nil
}

// HookOutput is the JSON a PreToolUse hook prints to tell Claude Code what to
// do: "allow" lets the call run, "deny" blocks it and shows the agent the
// reason.
func HookOutput(allow bool, reason string) []byte {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	})
	return b
}

// HookSettings returns the settings JSON that installs command as a PreToolUse
// hook for every tool, for the backend's --settings flag. timeoutSeconds is how
// long the backend waits for the hook: it has to outlast the operator's time to
// answer, or the backend gives up on the hook first.
func HookSettings(command string, timeoutSeconds int) string {
	b, _ := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "*",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": command,
					"timeout": timeoutSeconds,
				}},
			}},
		},
	})
	return string(b)
}
