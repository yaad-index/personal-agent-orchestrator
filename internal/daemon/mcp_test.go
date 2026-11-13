package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, n *node, tok string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: n.url + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{tok}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestTheMCPEndpointListsTheNodesAgentsAndRecipesToTheOperator(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()
	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "")
	require.NoError(t, err)

	cs := mcpSession(t, n, token)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_agents"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	raw, _ := json.Marshal(res.StructuredContent)
	var agents struct {
		Agents []struct {
			Name      string   `json:"name"`
			Tags      []string `json:"tags"`
			State     string   `json:"state"`
			Temporary bool     `json:"temporary"`
			Recipe    string   `json:"recipe"`
			Channel   string   `json:"channel"`
			Flag      string   `json:"flag"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(raw, &agents))
	require.Len(t, agents.Agents, 2)
	boss, tmp := agents.Agents[0], agents.Agents[1]
	assert.Equal(t, "boss", boss.Name, "a configured agent")
	assert.False(t, boss.Temporary)
	assert.Equal(t, "stopped", boss.State)
	assert.Empty(t, boss.Flag)
	assert.Equal(t, "tmp-a", tmp.Name, "and the temporary agent spawned over the other surface")
	assert.True(t, tmp.Temporary)
	assert.Equal(t, "fake", tmp.Recipe)
	assert.Equal(t, "local", tmp.Channel)
	assert.Equal(t, "running", tmp.State)
	assert.Equal(t, "active", tmp.Flag)
	assert.Equal(t, []string{"temp"}, tmp.Tags)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_recipes"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	raw, _ = json.Marshal(res.StructuredContent)
	assert.Contains(t, string(raw), `"name":"fake"`)
	assert.Contains(t, string(raw), "a fake agent")
}

func TestTheMCPEndpointRefusesAnyoneWithoutTheOperatorToken(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	for _, auth := range []string{"", "Bearer wrong", token} {
		req, err := http.NewRequest("POST", n.url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "auth %q", auth)
	}
}

func mcpCall[Out any](t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) Out {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out Out
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestTheOperatorSpawnsAndKillsATemporaryAgentOverMCP(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	cs := mcpSession(t, n, token)

	spawned := mcpCall[struct {
		Name string `json:"name"`
	}](t, cs, "spawn_agent", map[string]any{"recipe": "fake", "name": "tmp-a"})
	assert.Equal(t, "tmp-a", spawned.Name)
	a, ok := agentByName(t, n, "tmp-a")
	require.True(t, ok, "the agent is the same one the HTTP surface sees")
	assert.Equal(t, "running", a.State)

	// It is routable like any agent; then it makes something and is killed over MCP.
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "tmp-a")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: hi", n.next(t).Text)
	require.NoError(t, os.WriteFile(filepath.Join(n.stateDir, "temp", "tmp-a", "result.txt"), []byte("42"), 0o600))

	killed := mcpCall[struct {
		HarvestID string `json:"harvest_id"`
		Complete  bool   `json:"complete"`
		Files     int    `json:"files"`
	}](t, cs, "kill_agent", map[string]any{"name": "tmp-a"})
	assert.True(t, killed.Complete)
	assert.NotEmpty(t, killed.HarvestID)
	assert.GreaterOrEqual(t, killed.Files, 2, "README.md and result.txt at least")
	_, ok = agentByName(t, n, "tmp-a")
	assert.False(t, ok)
	_ = n.next(t) // the operator's harvest report on the channel

	list := mcpCall[struct {
		Harvests []struct {
			ID        string `json:"id"`
			Requester string `json:"requester"`
		} `json:"harvests"`
	}](t, cs, "list_harvests", nil)
	require.Len(t, list.Harvests, 1)
	assert.Equal(t, killed.HarvestID, list.Harvests[0].ID)
	assert.Equal(t, "operator", list.Harvests[0].Requester, "a kill over MCP with the operator's token is the operator's")

	read := mcpCall[struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}](t, cs, "read_harvest", map[string]any{"id": killed.HarvestID, "path": "result.txt"})
	assert.Equal(t, "42", read.Content)
	assert.Equal(t, "utf8", read.Encoding)

	// The same harvest through the HTTP surface: one store, two doors.
	b, err := n.client.HarvestFile(context.Background(), killed.HarvestID, "result.txt")
	require.NoError(t, err)
	assert.Equal(t, "42", string(b))
}

func TestMCPRefusalsCarryTheOperationsOwnWords(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	cs := mcpSession(t, n, token)

	for tool, args := range map[string]map[string]any{
		"spawn_agent":  {"recipe": "nope"},
		"kill_agent":   {"name": "boss"}, // a configured agent is not a temporary one
		"read_harvest": {"id": "no-such-id", "path": "x"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		assert.True(t, res.IsError, tool)
	}
	_, ok := agentByName(t, n, "boss")
	assert.False(t, ok)
}
