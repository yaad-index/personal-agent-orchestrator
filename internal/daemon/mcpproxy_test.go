package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

const upstreamCredEnv = "PAO_TEST_UPSTREAM_CREDENTIAL"

type queryArgs struct {
	Q string `json:"q"`
}

// searchUpstream is an upstream MCP server that wants a bearer token and has a
// tool pao offers and one it does not.
func searchUpstream(t *testing.T, token string) (url string, calls *atomic.Int32) {
	t.Helper()
	calls = &atomic.Int32{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "search", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "query", Description: "Finds things."}, func(_ context.Context, _ *mcp.CallToolRequest, a queryArgs) (*mcp.CallToolResult, any, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + a.Q}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "secret", Description: "not offered"}, func(_ context.Context, _ *mcp.CallToolRequest, _ queryArgs) (*mcp.CallToolResult, any, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "leaked"}}}, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, calls
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestAnAgentReachesAnUpstreamToolOnlyThroughAGrantAndPaoCallsItWithItsOwnCredential(t *testing.T) {
	url, calls := searchUpstream(t, "upstream-secret")
	t.Setenv(upstreamCredEnv, "upstream-secret")
	n := startNodeWith(t, map[string]string{"boss": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.MCPUpstreams = []config.MCPUpstream{{
			Name: "search", URL: url, CredentialEnv: upstreamCredEnv,
			Tools: map[string]config.UpstreamTool{"query": {Tags: []string{"research"}, Risk: gate.RiskRead}},
		}}
	})
	ctx := context.Background()
	_, set := os.LookupEnv(upstreamCredEnv)
	assert.False(t, set, "the credential is taken out of the environment agents inherit")

	_, err := n.client.Post(ctx, "human", "wake up", "name", "boss")
	require.NoError(t, err)
	_ = n.next(t)
	boss := mcpSession(t, n, sessionEnv(t, n, "boss", "PAO_AGENT_TOKEN"))

	assert.NotContains(t, toolNames(t, boss), "search__query", "not granted: not listed")
	_, err = boss.CallTool(ctx, &mcp.CallToolParams{Name: "search__query", Arguments: map[string]any{"q": "x"}})
	require.Error(t, err, "and not callable: it is not there")
	assert.Zero(t, calls.Load())

	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:boss", Kind: "tool", Tag: "research"})
	require.NoError(t, err)

	assert.Contains(t, toolNames(t, boss), "search__query", "granted, listed at once")
	assert.NotContains(t, toolNames(t, boss), "search__secret", "a tool the configuration does not name never is")
	res, err := boss.CallTool(ctx, &mcp.CallToolParams{Name: "search__query", Arguments: map[string]any{"q": "x"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Equal(t, "found x", res.Content[0].(*mcp.TextContent).Text, "a read-risk tool needs no question")
	assert.Equal(t, int32(1), calls.Load())

	// The operator is behind grants too: this one reaches agents, not the tool.
	assert.NotContains(t, toolNames(t, mcpSession(t, n, token)), "search__query")
}
