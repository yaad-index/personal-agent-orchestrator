//go:build contract

package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
)

// TestContract runs the real Claude Code CLI once and checks that the
// transcript it writes is still what this package reads: the file is where
// TranscriptDir says, every line parses, and a prompt and its answer come back
// as message entries. It spends one short model call, so it is opt-in:
//
//	go test -tags contract ./internal/backend/claudecode
//
// A failure here means a Claude Code upgrade changed the transcript format; fix
// the reader (and profile defaults) before the upgrade reaches an agent
// (ADR-0008 point 5).
func TestContract(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not installed")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// A fresh working directory gives a fresh transcript directory.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := TranscriptDir(home, cwd)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "Reply with exactly the single word: pong")
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "claude -p failed: %s", out)

	require.DirExists(t, dir, "the transcript directory is not where TranscriptDir says")

	entries, err := mirror.Replay(dir, Reader{})
	require.NoError(t, err, "a line of the real transcript no longer parses")

	var sawPrompt, sawAnswer bool
	for _, e := range entries {
		if e.Kind != core.KindMessage {
			continue
		}
		if e.Role == "user" && strings.Contains(e.Text, "single word: pong") {
			sawPrompt = true
		}
		if e.Role == "assistant" && strings.Contains(e.Text, "pong") {
			sawAnswer = true
		}
	}
	assert.True(t, sawPrompt, "the prompt did not come back as a user message entry")
	assert.True(t, sawAnswer, "the answer did not come back as an assistant message entry")
}
