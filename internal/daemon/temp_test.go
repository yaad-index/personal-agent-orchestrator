package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// tempRecipe is a recipe whose agent is the fake backend, writing its
// transcript into the workdir it is spawned in.
func tempRecipe(script, name, ready, format string) string {
	return fmt.Sprintf(`name: %s
description: a fake agent
profile:
  backend: fake
  tags: [temp]
  startup:
    command: "env TRANSCRIPT={workdir}/t.jsonl sh %s"
    ready_pattern: %q
    ready_timeout: 700ms
  transcript:
    format: %s
    path: "{workdir}/t.jsonl"
workdir:
  files:
    README.md: hello
tools: [Read]
`, name, script, ready, format)
}

func startTempNode(t *testing.T, recipes map[string]string, thresholds config.Temp, optFns ...func(*Options)) *node {
	t.Helper()
	return startNodeWith(t, map[string]string{"boss": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		for name, body := range recipes {
			body = strings.ReplaceAll(body, "@@SCRIPT@@", script)
			require.NoError(t, os.WriteFile(filepath.Join(rdir, name+".yaml"), []byte(body), 0o600))
		}
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Temp = thresholds
	}, optFns...)
}

func goodRecipe(name string) string { return tempRecipe("@@SCRIPT@@", name, "READY>", "claude-code") }

func tmuxHasSession(t *testing.T, n *node, name string) bool {
	t.Helper()
	// pao names its sessions "pao-<agent>".
	return osexec.Command("tmux", "-L", n.sock, "has-session", "-t", "=pao-"+name).Run() == nil
}

func agentByName(t *testing.T, n *node, name string) (local.AgentJSON, bool) {
	t.Helper()
	agents, err := n.client.Agents(context.Background())
	require.NoError(t, err)
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return local.AgentJSON{}, false
}

func TestATemporaryAgentIsSpawnedUsedAndKilledWithItsOutputHarvested(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()

	name, err := n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)
	assert.Equal(t, "tmp-a", name)

	a, ok := agentByName(t, n, "tmp-a")
	require.True(t, ok)
	assert.Equal(t, "fake", a.Recipe)
	assert.Equal(t, "local", a.Channel)
	assert.Equal(t, "running", a.State, "it was launched through the control plane at spawn")
	assert.Equal(t, "active", a.Flag)
	workdir := filepath.Join(n.stateDir, "temp", "tmp-a")
	assert.FileExists(t, filepath.Join(workdir, "README.md"))

	// It is an ordinary routable agent: a message to it by name, and by its recipe's tag.
	rep, err := n.client.Post(ctx, "human", "hello temp", "name", "tmp-a")
	require.NoError(t, err)
	assert.Empty(t, rep.Results[0].Error)
	got := n.next(t)
	assert.Equal(t, "tmp-a", got.Sender)
	assert.Equal(t, "echo: [human]: hello temp", got.Text)
	rep, err = n.client.Post(ctx, "human", "by tag", "tag", "temp")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Equal(t, "tmp-a", rep.Results[0].Agent)
	assert.Equal(t, "echo: [human]: by tag", n.next(t).Text)

	// It produces an output file, then is killed.
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "result.txt"), []byte("the answer"), 0o600))
	res, err := n.client.Kill(ctx, "tmp-a")
	require.NoError(t, err)
	assert.True(t, res.Harvest.Complete)
	assert.False(t, res.WorkdirKept)
	assert.Equal(t, "operator", res.Harvest.Requester)

	_, ok = agentByName(t, n, "tmp-a")
	assert.False(t, ok, "unregistered")
	assert.NoDirExists(t, workdir, "destroyed, after the harvest")
	assert.False(t, tmuxHasSession(t, n, "tmp-a"), "its session is gone")

	// The operator is told, privately.
	report := n.next(t)
	assert.Equal(t, "pao", report.Sender)
	assert.Contains(t, report.Text, res.Harvest.ID)
	assert.Contains(t, report.Text, "result.txt")

	// And the output is readable afterwards.
	b, err := n.client.HarvestFile(ctx, res.Harvest.ID, "result.txt")
	require.NoError(t, err)
	assert.Equal(t, "the answer", string(b))
	b, err = n.client.HarvestFile(ctx, res.Harvest.ID, "README.md")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	m, err := n.client.Harvest(ctx, res.Harvest.ID)
	require.NoError(t, err)
	assert.Equal(t, "tmp-a", m.Agent)
	assert.Equal(t, []string{"temp"}, m.Tags)
	hs, err := n.client.Harvests(ctx)
	require.NoError(t, err)
	require.Len(t, hs, 1)
	require.NoError(t, n.client.DeleteHarvest(ctx, res.Harvest.ID))
	_, err = n.client.Harvest(ctx, res.Harvest.ID)
	require.ErrorContains(t, err, "404")
}

func TestAKilledNameIsSpawnedAgainAndFollowedAfresh(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()

	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "one", "name", "tmp-a")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: one", n.next(t).Text)
	_, err = n.client.Kill(ctx, "tmp-a")
	require.NoError(t, err)
	_ = n.next(t) // the harvest report

	_, err = n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "two", "name", "tmp-a")
	require.NoError(t, err)
	got := n.next(t)
	assert.Equal(t, "echo: [human]: two", got.Text, "the second life's output arrives: its transcript is followed again")
}

func TestOutputCountsAsActivityForTheIdleFlag(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{UnusedAfter: time.Hour, IdleAfter: time.Second})
	ctx := context.Background()
	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "hi", "name", "tmp-a")
	require.NoError(t, err)
	_ = n.next(t)

	// 600ms on, the agent speaks without being spoken to.
	time.Sleep(600 * time.Millisecond)
	f, err := os.OpenFile(filepath.Join(n.stateDir, "temp", "tmp-a", "t.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"assistant","uuid":"x","message":{"role":"assistant","content":[{"type":"text","text":"unprompted"}]}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, "unprompted", n.next(t).Text)

	// 1.2s after the message but 0.6s after the agent's own output: still active.
	time.Sleep(600 * time.Millisecond)
	a, _ := agentByName(t, n, "tmp-a")
	assert.Equal(t, "active", a.Flag, "the agent's output is activity, not only the messages sent to it")

	require.Eventually(t, func() bool {
		a, _ := agentByName(t, n, "tmp-a")
		return a.Flag == "idle"
	}, 5*time.Second, 100*time.Millisecond, "and with no activity at all it is flagged idle, and only flagged")
	_, ok := agentByName(t, n, "tmp-a")
	assert.True(t, ok, "idle never removes an agent")
}

func TestANeverUsedAgentIsFlaggedUnusedAndKeptAlive(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{UnusedAfter: 300 * time.Millisecond, IdleAfter: time.Hour})
	_, err := n.client.Spawn(context.Background(), "fake", "tmp-a", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, _ := agentByName(t, n, "tmp-a")
		return a.Flag == "unused"
	}, 5*time.Second, 50*time.Millisecond)
	assert.True(t, tmuxHasSession(t, n, "tmp-a"), "flagged, not killed")
}

func TestSpawnAndKillErrorsHaveTheRightStatus(t *testing.T) {
	n := startTempNode(t, map[string]string{
		"fake":      goodRecipe("fake"),
		"badformat": tempRecipe("@@SCRIPT@@", "badformat", "READY>", "no-such-format"),
	}, config.Temp{})
	ctx := context.Background()
	status := func(err error, want string) {
		t.Helper()
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), want), "got %q, want a %s", err, want)
	}

	_, err := n.client.Spawn(ctx, "nope", "", "")
	status(err, "404")
	_, err = n.client.Spawn(ctx, "fake", "Bad Name", "")
	status(err, "400")
	_, err = n.client.Spawn(ctx, "fake", "tmp-a", "no-such-channel")
	status(err, "400")
	_, err = n.client.Spawn(ctx, "badformat", "tmp-b", "")
	status(err, "400")
	assert.NoDirExists(t, filepath.Join(n.stateDir, "temp", "tmp-b"), "nothing was left behind")
	_, err = n.client.Spawn(ctx, "fake", "boss", "")
	status(err, "409")

	_, err = n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)
	_, err = n.client.Spawn(ctx, "fake", "tmp-a", "")
	status(err, "409")

	_, err = n.client.Kill(ctx, "boss")
	status(err, "404")
	_, err = n.client.Kill(ctx, "missing")
	status(err, "404")
	_, ok := agentByName(t, n, "boss")
	assert.False(t, ok, "a configured agent is not listed as temporary and is not killable here")
	_, err = n.client.HarvestFile(ctx, "no-such-id", "x")
	status(err, "404")
}

func TestAnAgentThatNeverBecomesReadyIsReportedAndKillable(t *testing.T) {
	n := startTempNode(t, map[string]string{"stuck": tempRecipe("@@SCRIPT@@", "stuck", "NEVER-MATCHES", "claude-code")}, config.Temp{})
	ctx := context.Background()

	name, err := n.client.Spawn(ctx, "stuck", "tmp-s", "")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "502"), err.Error())
	assert.Equal(t, "tmp-s", name, "the name comes back so it can be inspected or killed")
	pane := n.next(t)
	assert.Contains(t, pane.Text, "not ready", "the operator is shown its pane, as for any agent")

	_, ok := agentByName(t, n, "tmp-s")
	assert.True(t, ok, "it stays until killed")
	res, err := n.client.Kill(ctx, "tmp-s")
	require.NoError(t, err, "an explicit kill removes it")
	assert.True(t, res.Harvest.Complete)
	_, ok = agentByName(t, n, "tmp-s")
	assert.False(t, ok)
}

func TestTheTempRoutesNeedTheToken(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/temp"}, {"POST", "/v1/temp"}, {"DELETE", "/v1/temp/x"},
		{"GET", "/v1/harvests"}, {"GET", "/v1/harvests/x"}, {"GET", "/v1/harvests/x/file?path=a"}, {"DELETE", "/v1/harvests/x"},
	} {
		for _, auth := range []string{"", "Bearer wrong", "test-token"} {
			req, err := http.NewRequest(c.method, n.url+c.path, strings.NewReader("{}"))
			require.NoError(t, err)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s with %q", c.method, c.path, auth)
		}
	}
}

func TestATempAgentSpeaksOnTheChannelItWasBoundTo(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()
	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "local")
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "x", "name", "tmp-a")
	require.NoError(t, err)
	got := n.next(t)
	assert.Equal(t, "tmp-a", got.Sender, "on the channel it was bound to")
}
