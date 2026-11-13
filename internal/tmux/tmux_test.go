package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDriver returns a Driver on a private tmux server that is torn down with
// the test. The tests run real tmux; without it they cannot say anything.
func newDriver(t *testing.T) *Driver {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("pao-test-%d", time.Now().UnixNano())
	d := New(WithSocket(sock), WithPasteSettle(50*time.Millisecond))
	t.Cleanup(func() { _, _ = d.run(context.Background(), nil, "kill-server") })
	return d
}

func waitFor(t *testing.T, d *Driver, agent, want string) string {
	t.Helper()
	var pane string
	require.Eventually(t, func() bool {
		var err error
		pane, err = d.Capture(context.Background(), agent)
		return err == nil && strings.Contains(pane, want)
	}, 5*time.Second, 50*time.Millisecond, "pane never contained %q", want)
	return pane
}

func TestSessionLifecycle(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()

	ok, err := d.HasSession(ctx, "a")
	require.NoError(t, err)
	assert.False(t, ok, "no server yet means no session, not an error")

	require.NoError(t, d.NewSession(ctx, "a", t.TempDir(), "cat", nil))
	ok, err = d.HasSession(ctx, "a")
	require.NoError(t, err)
	assert.True(t, ok)

	assert.Error(t, d.NewSession(ctx, "a", "", "cat", nil), "a second session of the same name is refused")

	require.NoError(t, d.KillSession(ctx, "a"))
	ok, err = d.HasSession(ctx, "a")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestNewSessionPassesItsEnvironmentToTheCommand(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "a", "", `sh -c 'echo "who=$PAO_TEST_WHO sock=$PAO_TEST_SOCK"; exec cat'`,
		[]string{"PAO_TEST_WHO=agent-a", "PAO_TEST_SOCK=/run/x y.sock"}))

	waitFor(t, d, "a", "who=agent-a sock=/run/x y.sock")
}

func TestHasSessionMatchesExactly(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "alpha", "", "cat", nil))

	ok, err := d.HasSession(ctx, "alp")
	require.NoError(t, err)
	assert.False(t, ok, "a name prefix must not match another agent's session")
}

func TestPasteArrivesVerbatim(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "a", "", "cat", nil))

	// Quotes, a dollar sign, a semicolon and a tmux key name must all be
	// typed as text, and the Enter must submit them (cat echoes the line).
	text := `[h]: say "hi" $HOME; Enter C-c`
	require.NoError(t, d.Paste(ctx, "a", text))

	pane := waitFor(t, d, "a", "C-c")
	assert.Equal(t, 2, strings.Count(pane, text), "typed once by the terminal, echoed once by cat after Enter")
}

func TestPasteMultiLine(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "a", "", "cat", nil))

	require.NoError(t, d.Paste(ctx, "a", "line one\nline two"))
	pane := waitFor(t, d, "a", "line two")
	assert.Contains(t, pane, "line one")
}

func TestCancelledPasteStillSubmitsSoTheNextOneIsNotGlued(t *testing.T) {
	d := newDriver(t)
	d.settle = 300 * time.Millisecond
	bg := context.Background()
	require.NoError(t, d.NewSession(bg, "a", "", "cat", nil))

	// The first paste's context ends during the settle pause, after the text
	// reached the pane and before the Enter.
	ctx, cancel := context.WithTimeout(bg, 100*time.Millisecond)
	defer cancel()
	require.NoError(t, d.Paste(ctx, "a", "FIRST-line"))
	require.NoError(t, d.Paste(bg, "a", "SECOND-line"))

	for _, want := range []string{"FIRST-line", "SECOND-line"} {
		require.Eventually(t, func() bool {
			pane, err := d.Capture(bg, "a")
			return err == nil && strings.Count(pane, want) == 2
		}, 5*time.Second, 50*time.Millisecond, "%s should be typed once and echoed once", want)
	}
	pane, err := d.Capture(bg, "a")
	require.NoError(t, err)
	assert.NotContains(t, pane, "FIRST-lineSECOND-line", "the two pastes must not be glued into one line")
}

func TestPasteWithAnAlreadyCancelledContextPastesNothing(t *testing.T) {
	d := newDriver(t)
	bg := context.Background()
	require.NoError(t, d.NewSession(bg, "a", "", "cat", nil))

	ctx, cancel := context.WithCancel(bg)
	cancel()
	assert.Error(t, d.Paste(ctx, "a", "NEVER-SENT"))

	time.Sleep(200 * time.Millisecond)
	pane, err := d.Capture(bg, "a")
	require.NoError(t, err)
	assert.NotContains(t, pane, "NEVER-SENT")
}

func TestConcurrentPastesToOneAgentDoNotInterleave(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "a", "", "cat", nil))

	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, d.Paste(ctx, "a", fmt.Sprintf("<<msg-%d-%s>>", i, strings.Repeat("x", 40))))
		}()
	}
	wg.Wait()

	// Each message is typed once and echoed once by cat, whole.
	for i := range n {
		want := fmt.Sprintf("<<msg-%d-%s>>", i, strings.Repeat("x", 40))
		require.Eventually(t, func() bool {
			pane, err := d.Capture(ctx, "a")
			return err == nil && strings.Count(pane, want) == 2
		}, 5*time.Second, 50*time.Millisecond, "message %d did not arrive whole", i)
	}
}

func TestOperationsOnMissingSession(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	require.NoError(t, d.NewSession(ctx, "a", "", "cat", nil))

	_, err := d.Capture(ctx, "ghost")
	assert.Error(t, err)
	assert.Error(t, d.Paste(ctx, "ghost", "x"))
	assert.Error(t, d.KillSession(ctx, "ghost"))
}
