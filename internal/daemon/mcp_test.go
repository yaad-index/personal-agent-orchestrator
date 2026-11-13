package daemon

import (
	"context"
	"encoding/json"
	"net/http"
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
