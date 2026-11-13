package control

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/tmux"
)

// fakeTerm is a scripted Terminal for the logic that does not need a terminal.
type fakeTerm struct {
	mu       sync.Mutex
	sessions map[string]bool
	panes    []string // successive captures; the last one repeats
	captures int
	started  []string
	env      [][]string
	pasted   []string
	killed   []string
}

func (f *fakeTerm) NewSession(_ context.Context, agent, _, command string, env []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = map[string]bool{}
	}
	f.sessions[agent] = true
	f.started = append(f.started, agent+":"+command)
	f.env = append(f.env, env)
	return nil
}

func (f *fakeTerm) HasSession(_ context.Context, agent string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[agent], nil
}

func (f *fakeTerm) KillSession(_ context.Context, agent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, agent)
	f.killed = append(f.killed, agent)
	return nil
}

func (f *fakeTerm) Capture(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.captures
	if i >= len(f.panes) {
		i = len(f.panes) - 1
	}
	f.captures++
	return f.panes[i], nil
}

func (f *fakeTerm) Paste(_ context.Context, agent, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pasted = append(f.pasted, agent+":"+text)
	return nil
}

func profile(timeout time.Duration) core.Profile {
	return core.Profile{
		Name: "a",
		Startup: core.Startup{
			Command:      "run",
			ReadyPattern: `READY>`,
			ReadyTimeout: timeout,
		},
	}
}

func TestLaunchWaitsForReadyPattern(t *testing.T) {
	ft := &fakeTerm{panes: []string{"loading", "still loading", "welcome\nREADY>"}}
	p := New(ft, WithPollInterval(time.Millisecond))

	require.NoError(t, p.Launch(context.Background(), profile(time.Second)))
	assert.Equal(t, []string{"a:run"}, ft.started)
	assert.Equal(t, 3, ft.captures, "polls until the pane matches, then stops")
}

func TestLaunchTimeoutKeepsPaneOffTheErrorText(t *testing.T) {
	secret := "enter your token: hunter2"
	ft := &fakeTerm{panes: []string{secret}}
	p := New(ft, WithPollInterval(time.Millisecond))

	err := p.Launch(context.Background(), profile(20*time.Millisecond))
	require.Error(t, err)

	re, ok := AsReadinessError(err)
	require.True(t, ok)
	assert.Equal(t, secret, re.Pane(), "the operator can read the raw pane")
	assert.NotContains(t, err.Error(), "hunter2", "the pane must not leak through the error text")
	assert.Empty(t, ft.killed, "a stalled startup is flagged, not killed")
	assert.True(t, ft.sessions["a"], "the session stays up for the operator")
}

func TestLaunchGivesTheAgentItsEnvironment(t *testing.T) {
	ft := &fakeTerm{panes: []string{"READY>"}}
	p := New(ft, WithPollInterval(time.Millisecond), WithEnv(func(agent string) []string {
		return []string{"WHO=" + agent}
	}))

	require.NoError(t, p.Launch(context.Background(), profile(time.Second)))
	assert.Equal(t, [][]string{{"WHO=a"}}, ft.env)
}

func TestLaunchDoesNotRestartAnExistingSession(t *testing.T) {
	ft := &fakeTerm{sessions: map[string]bool{"a": true}, panes: []string{"READY>"}}
	p := New(ft, WithPollInterval(time.Millisecond))

	require.NoError(t, p.Launch(context.Background(), profile(time.Second)))
	assert.Empty(t, ft.started)
}

func TestLaunchHonoursContextCancel(t *testing.T) {
	ft := &fakeTerm{panes: []string{"never"}}
	p := New(ft, WithPollInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	err := p.Launch(ctx, profile(time.Hour))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLaunchRejectsInvalidPattern(t *testing.T) {
	prof := profile(time.Second)
	prof.Startup.ReadyPattern = "("
	err := New(&fakeTerm{panes: []string{""}}).Launch(context.Background(), prof)
	assert.ErrorContains(t, err, "ready_pattern")
}

func TestDeliverPastesTheAgentLine(t *testing.T) {
	ft := &fakeTerm{}
	require.NoError(t, New(ft).Deliver(context.Background(), profile(0), core.Message{Sender: "h", Text: "hi"}))
	assert.Equal(t, []string{"a:[h]: hi"}, ft.pasted)
}

func TestKillIsIdempotent(t *testing.T) {
	ft := &fakeTerm{sessions: map[string]bool{"a": true}}
	p := New(ft)

	require.NoError(t, p.Kill(context.Background(), profile(0)))
	require.NoError(t, p.Kill(context.Background(), profile(0)))
	assert.Equal(t, []string{"a"}, ft.killed, "the second kill finds no session and does nothing")
}

func TestAsReadinessErrorIgnoresOtherErrors(t *testing.T) {
	_, ok := AsReadinessError(errors.New("other"))
	assert.False(t, ok)
	_, ok = AsReadinessError(fmt.Errorf("wrapped: %w", &ReadinessError{Agent: "a"}))
	assert.True(t, ok)
}

// The tests below run the control plane over real tmux.

func realPlane(t *testing.T) *Plane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("pao-ctl-test-%d", time.Now().UnixNano())
	d := tmux.New(tmux.WithSocket(sock), tmux.WithPasteSettle(50*time.Millisecond))
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
	})
	return New(d, WithPollInterval(25*time.Millisecond))
}

func realProfile(command string, timeout time.Duration) core.Profile {
	return core.Profile{
		Name: "a",
		Startup: core.Startup{
			Command:      command,
			ReadyPattern: `READY>`,
			ReadyTimeout: timeout,
		},
	}
}

func TestRealLaunchDeliverRestartKill(t *testing.T) {
	p := realPlane(t)
	ctx := context.Background()
	prof := realProfile(`sh -c "echo 'READY>'; exec cat"`, 5*time.Second)

	require.NoError(t, p.Launch(ctx, prof))
	require.NoError(t, p.Deliver(ctx, prof, core.Message{Sender: "h", Text: "ping"}))

	require.Eventually(t, func() bool {
		pane, err := p.term.Capture(ctx, "a")
		return err == nil && strings.Count(pane, "[h]: ping") == 2
	}, 5*time.Second, 50*time.Millisecond, "the line is typed once and echoed once")

	require.NoError(t, p.Restart(ctx, prof))
	pane, err := p.term.Capture(ctx, "a")
	require.NoError(t, err)
	assert.NotContains(t, pane, "ping", "a restart starts a fresh session")

	require.NoError(t, p.Kill(ctx, prof))
	exists, err := p.term.HasSession(ctx, "a")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRealReadinessTimeoutShowsThePane(t *testing.T) {
	p := realPlane(t)
	ctx := context.Background()
	prof := realProfile(`sh -c 'echo please log in; exec cat'`, 500*time.Millisecond)

	err := p.Launch(ctx, prof)
	re, ok := AsReadinessError(err)
	require.True(t, ok, "got %v", err)
	assert.Contains(t, re.Pane(), "please log in")

	exists, herr := p.term.HasSession(ctx, "a")
	require.NoError(t, herr)
	assert.True(t, exists, "the stalled session is left for the operator")

	// A second Launch finds the session and checks it again rather than failing
	// on a duplicate.
	err = p.Launch(ctx, prof)
	_, ok = AsReadinessError(err)
	assert.True(t, ok, "got %v", err)
}

func TestKillTellsTheOwnerOfAnythingTiedToTheSessionThatItEnded(t *testing.T) {
	ft := &fakeTerm{sessions: map[string]bool{"a": true}}
	var stopped []string
	p := New(ft, WithOnStop(func(a string) { stopped = append(stopped, a) }))

	require.NoError(t, p.Kill(context.Background(), profile(0)))
	assert.Equal(t, []string{"a"}, stopped)
	require.NoError(t, p.Kill(context.Background(), profile(0)))
	assert.Equal(t, []string{"a", "a"}, stopped, "also when the session was already gone: it is over either way")
}

type failingKill struct{ *fakeTerm }

func (failingKill) KillSession(context.Context, string) error { return errors.New("tmux says no") }

func TestAKillThatFailsDoesNotSayTheSessionEnded(t *testing.T) {
	ft := &fakeTerm{sessions: map[string]bool{"a": true}}
	var stopped []string
	p := New(failingKill{ft}, WithOnStop(func(a string) { stopped = append(stopped, a) }))
	require.Error(t, p.Kill(context.Background(), profile(0)))
	assert.Empty(t, stopped, "the session is still there, so what is tied to it must not be ended")
}

func TestAliveIsTrueOnlyForAnAgentWithASession(t *testing.T) {
	ft := &fakeTerm{sessions: map[string]bool{"a": true}}
	p := New(ft)
	assert.True(t, p.Alive(context.Background(), "a"))
	assert.False(t, p.Alive(context.Background(), "b"))
}

type erroringHas struct{ *fakeTerm }

func (erroringHas) HasSession(context.Context, string) (bool, error) {
	return true, errors.New("tmux unreachable")
}

func TestAnErrorAskingIsNotAlive(t *testing.T) {
	assert.False(t, New(erroringHas{&fakeTerm{}}).Alive(context.Background(), "a"), "a question that cannot be answered is not a yes")
}
