package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
)

// noticesAfterStop stops the node and returns the text of every notice pao
// stored, oldest first. A notice sent at start can go out before a test's
// stream is open again, so it is read from the store.
func noticesAfterStop(t *testing.T, n *node) []string {
	t.Helper()
	n.stop(t)
	store, err := msgstore.Open(filepath.Join(n.stateDir, "messages.db"), n.cfg.Orchestrator.MessageWindow)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	var out []string
	for i := 1; i <= n.cfg.Orchestrator.MessageWindow; i++ {
		m, err := store.Get(fmt.Sprintf("m%d", i))
		if err != nil {
			continue
		}
		if m.Kind == core.MessageSystemEvent {
			out = append(out, m.Text)
		}
	}
	return out
}

func TestARestartedPaoTakesBackARunningAgentAsItWas(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: hi", n.next(t).Text)
	pid := panePID(t, n, "a")
	token := agentToken(t, n, "a")

	n.restart(t)

	assert.Equal(t, pid, panePID(t, n, "a"), "the agent was not started again")
	assert.Equal(t, token, agentToken(t, n, "a"), "it keeps the token it was started with")
	assert.Equal(t, http.StatusOK, mcpStatus(t, n.url, token), "and pao accepts its calls with it")

	_, err = n.client.Post(context.Background(), "human", "again", "name", "a")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: again", n.next(t).Text, "its transcript is followed from where it stood, nothing replayed")

	require.NoError(t, n.client.AgentAction(context.Background(), "a", "start"))
	assert.Equal(t, pid, panePID(t, n, "a"), "pao counts it as running: a start leaves it be")
}

func TestASessionPaoHasNoRecordOfIsReportedAndLeftRunning(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	out, err := exec.Command("tmux", "-L", n.sock, "new-session", "-d", "-s", "pao-ghost", "sleep 60").CombinedOutput()
	require.NoError(t, err, string(out))
	stale := filepath.Join(n.stateDir, "agent-tokens", "ghost")
	require.NoError(t, os.WriteFile(stale, []byte(strings.Repeat("ab", 32)), 0o600))

	n.restart(t)

	_, err = exec.Command("tmux", "-L", n.sock, "has-session", "-t", "=pao-ghost").CombinedOutput()
	assert.NoError(t, err, "left running, never killed")
	assert.NoFileExists(t, stale, "a token of no agent pao took back is let go")
	notices := strings.Join(noticesAfterStop(t, n), "\n")
	assert.Contains(t, notices, "no record of")
	assert.Contains(t, notices, ": ghost.", "the operator is told which session")
}

func TestAnAgentTakenBackWithoutItsTokenIsCalledOut(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)
	n.next(t)
	token := agentToken(t, n, "a")
	pid := panePID(t, n, "a")
	require.NoError(t, os.Remove(filepath.Join(n.stateDir, "agent-tokens", "a")))

	n.restart(t)

	assert.Equal(t, pid, panePID(t, n, "a"), "still taken back, not started again")
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, token), "a token pao no longer holds is refused")
	assert.Contains(t, strings.Join(noticesAfterStop(t, n), "\n"), "without its token, so its calls to pao are refused until it is restarted: a.", "and the operator is told")
}

// fakeFollow records the agents whose transcripts are followed.
type fakeFollow struct{ started []string }

func (f *fakeFollow) start(p core.Profile) { f.started = append(f.started, p.Name) }

type fakeTokens map[string]string

func (f fakeTokens) Restore(agent, token string) { f[agent] = token }

func TestAdoptionTakesBackOnlyConfiguredAgentsWithALiveSession(t *testing.T) {
	files := agentTokenFiles{dir: t.TempDir()}
	tok := strings.Repeat("cd", 32)
	require.NoError(t, files.write("a", tok))
	require.NoError(t, files.write("ghost", tok))
	follow, tokens := &fakeFollow{}, fakeTokens{}
	var running []string
	found, err := adopter{
		sessions: func(context.Context) ([]string, error) { return []string{"ghost", "b", "a"}, nil },
		tokens:   tokens,
		files:    files,
		follow:   follow,
		running:  func(a string) error { running = append(running, a); return nil },
	}.adopt(context.Background(), []core.Profile{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"a", "b"}, found.adopted, "configured agents with a session; c has none")
	assert.Equal(t, []string{"b"}, found.tokenless)
	assert.Equal(t, []string{"ghost"}, found.unknown, "a session of no configured agent")
	assert.ElementsMatch(t, []string{"a", "b"}, follow.started)
	assert.ElementsMatch(t, []string{"a", "b"}, running)
	assert.Equal(t, fakeTokens{"a": tok}, tokens, "a token is restored only from the agent's own file")
	assert.Equal(t, map[string]bool{"a": true}, found.keep())

	notice := found.notice("sock")
	assert.Contains(t, notice, "without its token")
	assert.Contains(t, notice, ": b.")
	assert.Contains(t, notice, "no record of")
	assert.Contains(t, notice, ": ghost.")
	assert.Contains(t, notice, "tmux -L sock kill-session")
	assert.Empty(t, adoption{adopted: []string{"a"}}.notice("sock"), "a clean take-back tells no one")
}

func TestATokenFileThatHoldsNoTokenIsNotRestored(t *testing.T) {
	files := agentTokenFiles{dir: t.TempDir()}
	require.NoError(t, os.WriteFile(files.path("a"), []byte("not a token"), 0o600))
	tokens := fakeTokens{}
	found, err := adopter{
		sessions: func(context.Context) ([]string, error) { return []string{"a"}, nil },
		tokens:   tokens, files: files, follow: &fakeFollow{},
		running: func(string) error { return nil },
	}.adopt(context.Background(), []core.Profile{{Name: "a"}})
	require.NoError(t, err)
	assert.Empty(t, tokens)
	assert.Equal(t, []string{"a"}, found.tokenless)
}

func TestPruneKeepsOnlyTheNamedTokens(t *testing.T) {
	files := agentTokenFiles{dir: t.TempDir()}
	require.NoError(t, files.write("a", "x"))
	require.NoError(t, files.write("b", "y"))
	require.NoError(t, files.prune(map[string]bool{"a": true}))
	assert.FileExists(t, files.path("a"))
	assert.NoFileExists(t, files.path("b"))
}
