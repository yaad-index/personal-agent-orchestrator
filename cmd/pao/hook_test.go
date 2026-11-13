package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

type answerer struct{ ans gate.Answer }

func (a answerer) Ask(context.Context, gate.Request, string) (gate.Answer, error) { return a.ans, nil }

func gateSocket(t *testing.T, asker gate.Asker) string {
	t.Helper()
	pol, err := gate.LoadPolicy(filepath.Join(t.TempDir(), "p.json"))
	require.NoError(t, err)
	g := &gate.Gate{Policy: pol, Asker: asker, AutoAllow: gate.DefaultAutoAllow(), AskTimeout: time.Second}
	sock := filepath.Join(t.TempDir(), "g.sock")
	ln, err := gate.Listen(sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: (&gate.Server{Gate: g}).Handler(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

type out struct {
	Out struct {
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

func hook(t *testing.T, stdin, agent, socket string) out {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runHook(context.Background(), strings.NewReader(stdin), &buf, agent, socket))
	var o out
	require.NoError(t, json.Unmarshal(buf.Bytes(), &o), buf.String())
	return o
}

const bashLs = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
const readX = `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/x"}}`

func TestHookAllowsARead(t *testing.T) {
	o := hook(t, readX, "a", gateSocket(t, nil))
	assert.Equal(t, "allow", o.Out.Decision)
}

func TestHookDeniesWhenTheOperatorDenies(t *testing.T) {
	o := hook(t, bashLs, "a", gateSocket(t, answerer{gate.Answer{Allow: false, Reason: "not now"}}))
	assert.Equal(t, "deny", o.Out.Decision)
	assert.Contains(t, o.Out.Reason, "not now")
	assert.Contains(t, o.Out.Reason, "Do not retry", "the agent is told to stay actionable, not loop")
}

func TestHookAllowsWhenTheOperatorAllows(t *testing.T) {
	o := hook(t, bashLs, "a", gateSocket(t, answerer{gate.Answer{Allow: true}}))
	assert.Equal(t, "allow", o.Out.Decision)
	assert.NotContains(t, o.Out.Reason, "Do not retry")
}

func TestHookFailsClosedWhenNoGateIsConfigured(t *testing.T) {
	for _, tc := range [][2]string{{"", "/s"}, {"a", ""}, {"", ""}} {
		o := hook(t, readX, tc[0], tc[1])
		assert.Equal(t, "deny", o.Out.Decision, "agent=%q socket=%q", tc[0], tc[1])
	}
}

func TestHookFailsClosedWhenTheGateIsDown(t *testing.T) {
	o := hook(t, readX, "a", filepath.Join(t.TempDir(), "absent.sock"))
	assert.Equal(t, "deny", o.Out.Decision, "even a read is denied when the gate cannot be reached")
	assert.Contains(t, o.Out.Reason, "could not be reached")
}

func TestHookFailsClosedOnUnreadableInput(t *testing.T) {
	sock := gateSocket(t, answerer{gate.Answer{Allow: true}})
	for _, in := range []string{``, `{`, `{"hook_event_name":"PreToolUse"}`, `{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{}}`} {
		o := hook(t, in, "a", sock)
		assert.Equal(t, "deny", o.Out.Decision, "input %q", in)
	}
}

func TestHookSettingsTimeoutOutlastsTheGatesAskTimeout(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir)
	require.NoError(t, os.WriteFile(path, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_RUN_TEST_TOKEN\ngate:\n  ask_timeout: 20m\n"), 0o600))

	got, err := HookSettingsCmd{Config: path, Timeout: time.Minute}.timeout()
	require.NoError(t, err)
	assert.Equal(t, 20*time.Minute+hookMargin, got, "longer than the gate waits, whatever --timeout says")

	got, err = HookSettingsCmd{Timeout: 7 * time.Minute}.timeout()
	require.NoError(t, err)
	assert.Equal(t, 7*time.Minute, got, "without a config the flag is used")

	require.NoError(t, os.WriteFile(path, []byte("node: n1\n"), 0o600))
	_, err = HookSettingsCmd{Config: path}.timeout()
	assert.Error(t, err, "an unloadable config is an error, not a silent default")
}
