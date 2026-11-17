package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

type args struct {
	Q string `json:"q"`
}

// fakeUpstream is an MCP server that wants a bearer token and counts calls.
type fakeUpstream struct {
	url   string
	calls atomic.Int32
}

func newFakeUpstream(t *testing.T, token string) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "query", Description: "Finds. " + strings.Repeat("é", MaxDescription)}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + a.Q}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "big", Description: "too much"}, func(_ context.Context, _ *mcp.CallToolRequest, _ args) (*mcp.CallToolResult, any, error) {
		f.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", MaxResult)}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "secret", Description: "not offered"}, func(_ context.Context, _ *mcp.CallToolRequest, _ args) (*mcp.CallToolResult, any, error) {
		f.calls.Add(1)
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
	f.url = ts.URL
	return f
}

func conf(url string) config.MCPUpstream {
	return config.MCPUpstream{Name: "search", URL: url, Tools: map[string]config.UpstreamTool{
		"query": {Tags: []string{"research"}, Risk: gate.RiskRead},
		"big":   {},
		"gone":  {},
	}}
}

func TestOnlyTheConfiguredToolsAreOfferedWithTheConfigurationsTagsAndRisk(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "tok")
	t.Cleanup(func() { _ = s.Close() })

	tools, missing, err := s.Tools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2, "secret is the server's, not offered")
	assert.Equal(t, "search__big", tools[0].Name)
	q := tools[1]
	assert.Equal(t, "search__query", q.Name)
	assert.Equal(t, "search", q.Upstream)
	assert.Equal(t, "query", q.Remote)
	assert.Equal(t, []string{"research"}, q.Tags)
	assert.Equal(t, gate.RiskRead, q.Risk)
	assert.True(t, strings.HasPrefix(q.Description, "[from upstream search] Finds. "), "marked as the server's")
	assert.LessOrEqual(t, len(q.Description), len("[from upstream search] ")+MaxDescription+len("…"), "and capped")
	assert.True(t, strings.HasSuffix(q.Description, "…"))
	assert.True(t, utf8.ValidString(q.Description), "cut on a character, not inside one")
	assert.Equal(t, []string{"gone"}, missing, "a configured tool the server lacks is reported")
}

func TestACallReachesTheServerWithTheCredential(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "tok")
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Call(context.Background(), "query", map[string]any{"q": "pao"})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "found pao", res.Content[0].(*mcp.TextContent).Text)
	assert.Equal(t, int32(1), f.calls.Load())
}

func TestAToolTheConfigurationDoesNotNameIsNeverCalled(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "tok")
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "secret", map[string]any{"q": "x"})
	assert.ErrorIs(t, err, ErrNotOffered)
	assert.Equal(t, int32(0), f.calls.Load())
}

func TestAResultPastTheCapIsRefusedNotCut(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "tok")
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "big", map[string]any{"q": "all"})
	assert.ErrorIs(t, err, ErrTooLarge)
	assert.Equal(t, int32(1), f.calls.Load(), "it ran; its answer is what was refused")
}

func TestAWrongCredentialIsAnErrorInPaosWordsWithoutTheToken(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "WRONG-SECRET")
	t.Cleanup(func() { _ = s.Close() })
	_, _, err := s.Tools(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `upstream "search" could not connect`)
	assert.NotContains(t, err.Error(), "WRONG-SECRET")
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "x"})
	require.Error(t, err, "and every use tries again rather than keeping a broken session")
	assert.Equal(t, int32(0), f.calls.Load())
}

func TestAServerThatGoesAwayIsReconnectedOnTheNextUse(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), "tok")
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "query", map[string]any{"q": "1"})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "2"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), f.calls.Load())
}

func TestAServerThatForgetsTheSessionFailsOneCallAndTheNextReconnects(t *testing.T) {
	var calls atomic.Int32
	newHandler := func() http.Handler {
		srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "query"}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
			calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + a.Q}}}, nil, nil
		})
		// Stateful, as a server that keeps sessions is: a restart forgets them.
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	}
	var current atomic.Value
	current.Store(newHandler())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().(http.Handler).ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	s := New(conf(ts.URL), "")
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "query", map[string]any{"q": "1"})
	require.NoError(t, err)

	current.Store(newHandler()) // the server restarts
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "2"})
	require.Error(t, err, "the old session is gone; the call is not repeated on a new one")
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "3"})
	require.NoError(t, err, "the next use connects afresh")
	assert.Equal(t, int32(2), calls.Load())
}
