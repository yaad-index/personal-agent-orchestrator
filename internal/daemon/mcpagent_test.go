package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	osexec "os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// agentsMayDoAnything is a stand-in for the access matrix that lets every agent
// through the seam, so that what is under test is who a call is made as, and the
// approval gate that still stands behind spawn and kill.
type agentsMayDoAnything struct{}

func (agentsMayDoAnything) Allow(s access.Subject, _ access.Action, _ access.Resource) bool {
	return s.Kind == access.KindOperator || s.Kind == access.KindAgent
}

func withAgentsAllowed(o *Options) { o.Authorizer = agentsMayDoAnything{} }

// sessionEnv reads one variable out of an agent's tmux session environment, the
// way anything able to look at it could.
func sessionEnv(t *testing.T, n *node, agent, name string) string {
	t.Helper()
	out, err := osexec.Command("tmux", "-L", n.sock, "show-environment", "-t", "=pao-"+agent, name).CombinedOutput()
	require.NoError(t, err, string(out))
	line := strings.TrimSpace(string(out))
	v, ok := strings.CutPrefix(line, name+"=")
	require.True(t, ok, "got %q", line)
	return v
}

func mcpStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func spawnTwo(t *testing.T, n *node) {
	t.Helper()
	for _, name := range []string{"tmp-a", "tmp-b"} {
		_, err := n.client.Spawn(context.Background(), "fake", name, "")
		require.NoError(t, err)
	}
}

func TestEveryAgentIsStartedWithItsOwnTokenAndTheNodesAddressButNotTheOperatorsToken(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)

	a, b := sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"), sessionEnv(t, n, "tmp-b", "PAO_AGENT_TOKEN")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), a)
	assert.NotEqual(t, a, b, "one token per agent")
	assert.NotEqual(t, token, a)
	assert.Equal(t, n.url+"/mcp", sessionEnv(t, n, "tmp-a", "PAO_MCP_URL"))
	assert.Equal(t, "tmp-a", sessionEnv(t, n, "tmp-a", "PAO_AGENT"))

	// A configured agent gets one too, when its session starts.
	_, err := n.client.Post(context.Background(), "human", "wake up", "name", "boss")
	require.NoError(t, err)
	_ = n.next(t)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), sessionEnv(t, n, "boss", "PAO_AGENT_TOKEN"))
}

func TestAnAgentsTokenIsTheAgentNotTheOperator(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)
	agent := mcpSession(t, n, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"))

	// With the default seam an agent sees and can do nothing; the operator
	// would see both agents and boss.
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_agents"})
	require.NoError(t, err)
	raw, _ := json.Marshal(res.StructuredContent)
	assert.JSONEq(t, `{"agents":[]}`, string(raw))

	for tool, args := range map[string]map[string]any{
		"send_message": {"name": "tmp-b", "text": "hi"},
		"spawn_agent":  {"recipe": "fake"},
		"kill_agent":   {"name": "tmp-b"},
		"read_harvest": {"id": "x"},
	} {
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		assert.True(t, res.IsError, "%s is refused to an agent by the default seam", tool)
	}
	_, ok := agentByName(t, n, "tmp-b")
	assert.True(t, ok, "and nothing happened")
}

func TestAnAgentSpeaksAsItselfOnceTheSeamAllowsIt(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{}, withAgentsAllowed)
	spawnTwo(t, n)
	a := mcpSession(t, n, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"))

	out := mcpCall[struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}](t, a, "list_agents", nil)
	assert.GreaterOrEqual(t, len(out.Agents), 3, "the seam let the agent through, so it sees the agents")

	// Its message to a peer reads as from tmp-a: the identity came from its token.
	_ = mcpCall[struct {
		Delivered []struct{ Agent string } `json:"delivered"`
	}](t, a, "send_message", map[string]any{"name": "tmp-b", "text": "psst"})
	assert.Equal(t, "echo: [tmp-a]: psst", n.next(t).Text, "the peer read the sender's own name")

	// tmp-b cannot speak as tmp-a with its own token, and tmp-a's token does not
	// become tmp-b's.
	b := mcpSession(t, n, sessionEnv(t, n, "tmp-b", "PAO_AGENT_TOKEN"))
	_ = mcpCall[struct {
		Delivered []struct{ Agent string } `json:"delivered"`
	}](t, b, "send_message", map[string]any{"name": "tmp-a", "text": "back"})
	assert.Equal(t, "echo: [tmp-b]: back", n.next(t).Text)
}

func TestAnAgentsKillOfAPeerWaitsForTheOperatorsAnswer(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{}, withAgentsAllowed)
	spawnTwo(t, n)
	a := mcpSession(t, n, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"))

	type result struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := a.CallTool(context.Background(), &mcp.CallToolParams{Name: "kill_agent", Arguments: map[string]any{"name": "tmp-b"}})
		done <- result{res, err}
	}()

	// The operator is asked on the channel, privately, and nothing is killed meanwhile.
	q := n.next(t)
	require.Equal(t, "pao", q.Sender)
	require.Contains(t, q.Text, `agent "tmp-a" wants to run pao.kill`)
	id := regexp.MustCompile(`/allow (q\d+)`).FindStringSubmatch(q.Text)
	require.Len(t, id, 2, q.Text)
	select {
	case r := <-done:
		t.Fatalf("the kill went through without an answer: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	_, ok := agentByName(t, n, "tmp-b")
	require.True(t, ok)

	require.NoError(t, n.client.Decide(context.Background(), id[1], "allow", ""))
	r := <-done
	require.NoError(t, r.err)
	require.False(t, r.res.IsError, "%+v", r.res.Content)
	_, ok = agentByName(t, n, "tmp-b")
	assert.False(t, ok, "killed once the operator allowed it")

	hs, err := n.client.Harvests(context.Background())
	require.NoError(t, err)
	require.Len(t, hs, 1)
	assert.Equal(t, "agent:tmp-a", hs[0].Requester, "the harvest records who asked: the agent, by its token's identity")
}

func TestADeniedAgentKillKillsNothing(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{}, withAgentsAllowed)
	spawnTwo(t, n)
	a := mcpSession(t, n, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"))

	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := a.CallTool(context.Background(), &mcp.CallToolParams{Name: "kill_agent", Arguments: map[string]any{"name": "tmp-b"}})
		done <- res
	}()
	q := n.next(t)
	id := regexp.MustCompile(`/allow (q\d+)`).FindStringSubmatch(q.Text)
	require.Len(t, id, 2)
	require.NoError(t, n.client.Decide(context.Background(), id[1], "deny", "not this one"))

	res := <-done
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	assert.Contains(t, text.String(), "not this one", "the operator's reason reaches the agent")
	_, ok := agentByName(t, n, "tmp-b")
	assert.True(t, ok)
	assert.True(t, tmuxHasSession(t, n, "tmp-b"))
}

func TestATokenEndsWithItsAgentsSession(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)
	tokA := sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN")
	tokB := sessionEnv(t, n, "tmp-b", "PAO_AGENT_TOKEN")
	assert.Equal(t, http.StatusOK, mcpStatus(t, n.url, tokA))

	_, err := n.client.Kill(context.Background(), "tmp-a")
	require.NoError(t, err)
	_ = n.next(t)
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, tokA), "killed: its token is revoked")
	assert.Equal(t, http.StatusOK, mcpStatus(t, n.url, tokB), "another agent's token is untouched")

	// The name respawned is a new session with a new token; the old one stays dead.
	_, err = n.client.Spawn(context.Background(), "fake", "tmp-a", "")
	require.NoError(t, err)
	fresh := sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN")
	assert.NotEqual(t, tokA, fresh)
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, tokA))
	assert.Equal(t, http.StatusOK, mcpStatus(t, n.url, fresh))
}

func TestATokenStopsWorkingWhenItsSessionDiesEvenIfNobodyRevokedIt(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)
	tok := sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN")
	require.Equal(t, http.StatusOK, mcpStatus(t, n.url, tok))

	// The session ends on its own, not through a kill.
	require.NoError(t, osexec.Command("tmux", "-L", n.sock, "kill-session", "-t", "=pao-tmp-a").Run())
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, tok), "an identity tied to a live session ends with it")
}

func TestTheOperatorsTokenStillWorksAndAnAgentsIsNotTheOperators(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)
	assert.Equal(t, http.StatusOK, mcpStatus(t, n.url, token))
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, "PAO_AGENT_TOKEN"))
	assert.Equal(t, http.StatusUnauthorized, mcpStatus(t, n.url, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN")[:60]))
}
