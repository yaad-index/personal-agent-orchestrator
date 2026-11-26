package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/egress/confine"
)

// TestAPaneCommandParsesBackToItsSpec runs the pane command the daemon builds
// through a real shell's word splitting and this command line's parser, so the
// two cannot drift apart.
func TestAPaneCommandParsesBackToItsSpec(t *testing.T) {
	spec := confine.Spec{
		Socket:   "/state/egress/one-1/proxy.sock",
		Mask:     []string{"/state", "/run/user/1000", "/odd path/it's, here"},
		Keep:     []string{"/state/gate.sock", "/state/temp/one-1"},
		Forwards: []confine.Forward{{Addr: "127.0.0.1:7433", Socket: "/state/mcp.sock"}},
	}
	inner := `claude --append-system-prompt 'say "hi"' --x`
	pane := confine.PaneCommand("/opt/pao", spec, inner)
	require.True(t, strings.HasPrefix(pane, "exec "))
	out, err := exec.Command("sh", "-c", "set -- "+strings.TrimPrefix(pane, "exec ")+`; printf '%s\0' "$@"`).Output()
	require.NoError(t, err)
	words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.Equal(t, "/opt/pao", words[0])

	var cli CLI
	parser, err := kong.New(&cli, kong.Name("pao"))
	require.NoError(t, err)
	_, err = parser.Parse(words[1:])
	require.NoError(t, err)
	got := cli.EgressExec
	assert.Equal(t, spec.Socket, got.Socket)
	assert.Equal(t, spec.Mask, got.Mask)
	assert.Equal(t, spec.Keep, got.Keep)
	assert.Equal(t, []string{"127.0.0.1:7433=/state/mcp.sock"}, got.Forward)
	assert.Equal(t, []string{"--", "sh", "-c", inner}, got.Command)
}
