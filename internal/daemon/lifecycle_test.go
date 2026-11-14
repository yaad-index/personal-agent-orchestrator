package daemon

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

const stuckAgent = `sh -c "echo please log in to continue; exec cat"`

// panePID is the process id of the agent's pane, which changes when the session is
// started again.
func panePID(t *testing.T, n *node, name string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", n.sock, "list-panes", "-t", "=pao-"+name, "-F", "#{pane_pid}").Output()
	require.NoError(t, err, "no session for %s", name)
	return strings.TrimSpace(string(out))
}

func TestTheOperatorStartsStopsAndRestartsAConfiguredAgentOnThisNode(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	ctx := context.Background()
	require.False(t, tmuxHasSession(t, n, "bee"), "an agent starts with its first message")

	require.NoError(t, n.client.AgentAction(ctx, "bee", "start"))
	require.True(t, tmuxHasSession(t, n, "bee"))
	first := panePID(t, n, "bee")
	require.NoError(t, n.client.AgentAction(ctx, "bee", "start"), "starting a running agent is not an error")
	assert.Equal(t, first, panePID(t, n, "bee"), "and does not start it twice")

	require.NoError(t, n.client.AgentAction(ctx, "bee", "restart"))
	second := panePID(t, n, "bee")
	assert.NotEqual(t, first, second, "a restart is a new session")

	require.NoError(t, n.client.AgentAction(ctx, "bee", "stop"))
	assert.False(t, tmuxHasSession(t, n, "bee"))

	rep, err := n.client.Post(ctx, "human", "back", "name", "bee")
	require.NoError(t, err)
	assert.True(t, rep.Results[0].Launched, "the next message starts it again")
	assert.Equal(t, "echo: [human]: back", n.next(t).Text)
}

func TestAnAgentActionThatCannotBeDoneIsRefusedWithAReason(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()
	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "", "")
	require.NoError(t, err)

	for _, c := range []struct {
		name, agent, action, code, words string
	}{
		{"unknown", "ghost", "start", "404", `no agent "ghost"`},
		{"unknown stop", "ghost", "stop", "404", "no agent"},
		{"temporary start", "tmp-a", "start", "409", "started by spawning"},
		{"temporary stop", "tmp-a", "stop", "409", "pao kill"},
		{"temporary restart", "tmp-a", "restart", "409", "temporary agent"},
		{"no such action", "boss", "explode", "404", "start, stop or restart"},
	} {
		err := n.client.AgentAction(ctx, c.agent, c.action)
		require.Error(t, err, c.name)
		assert.Contains(t, err.Error(), c.code, c.name)
		assert.Contains(t, err.Error(), c.words, c.name)
	}
	assert.True(t, tmuxHasSession(t, n, "tmp-a"), "a refused stop left the temporary agent running")
}

func TestAnAgentThatIsStartedButNeverReadyIsReportedAndStaysStoppable(t *testing.T) {
	n := startNode(t, map[string]string{"stuck": stuckAgent})
	ctx := context.Background()

	err := n.client.AgentAction(ctx, "stuck", "start")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
	assert.Contains(t, n.next(t).Text, "not ready", "the operator is shown its pane, as for any agent")
	assert.True(t, tmuxHasSession(t, n, "stuck"), "it exists, so it can be inspected")
	require.NoError(t, n.client.AgentAction(ctx, "stuck", "stop"))
	assert.False(t, tmuxHasSession(t, n, "stuck"))
}

func TestTheAgentActionRouteNeedsTheToken(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	for _, auth := range []string{"", "Bearer wrong", token} {
		req, err := http.NewRequest("POST", n.url+"/v1/agents/bee/start", nil)
		require.NoError(t, err)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "with %q", auth)
	}
	assert.False(t, tmuxHasSession(t, n, "bee"), "nothing started")
}

func TestTheMasterStartsStopsAndRestartsAnAgentOnAnotherNode(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, map[string]string{"fake": goodRecipe("fake")})
	directoryHas(t, a, "n-b", 1)
	ctx := context.Background()

	require.NoError(t, a.client.AgentAction(ctx, "bee", "start"))
	assert.True(t, tmuxHasSession(t, b, "bee"), "started on its own node")
	assert.False(t, tmuxHasSession(t, a, "bee"))
	first := panePID(t, b, "bee")
	require.NoError(t, a.client.AgentAction(ctx, "bee", "restart"))
	assert.NotEqual(t, first, panePID(t, b, "bee"))
	require.NoError(t, a.client.AgentAction(ctx, "bee", "stop"))
	assert.False(t, tmuxHasSession(t, b, "bee"))

	_, err := a.client.Spawn(ctx, "fake", "tmp-r", "", "n-b")
	require.NoError(t, err)
	directoryHas(t, a, "n-b", 2)
	err = a.client.AgentAction(ctx, "tmp-r", "stop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409", "a temporary agent is killed the way that harvests it")
	assert.True(t, tmuxHasSession(t, b, "tmp-r"))
	err = a.client.AgentAction(ctx, "tmp-r", "start")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409", "nor started outside the spawn that provisions it")

	err = a.client.AgentAction(ctx, "ghost", "start")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), `no agent "ghost" in the mesh`, "the master asked the directory")
	require.NoError(t, b.client.AgentAction(ctx, "bee", "start"), "its own node's agent, with no master")
}

func TestARemoteStartThatIsNeverReadyIsAGatewayError(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"stuck": stuckAgent}, nil)
	directoryHas(t, a, "n-b", 1)
	ctx := context.Background()

	err := a.client.AgentAction(ctx, "stuck", "start")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
	assert.True(t, tmuxHasSession(t, b, "stuck"))
	require.NoError(t, a.client.AgentAction(ctx, "stuck", "stop"))
}

func TestASecondaryDoesNotCommandAnotherNodesAgents(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", map[string]string{"boss": ""}, nil)
	b := f.startWith(t, "n-b", nil, nil)
	directoryHas(t, a, "n-b", 0)
	err := b.client.AgentAction(context.Background(), "boss", "start")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404", "the master's agent is not this node's, and a secondary has no directory")
	assert.False(t, tmuxHasSession(t, a, "boss"))
}
