package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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
	s := New(conf(f.url), fixed("tok"))
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
	s := New(conf(f.url), fixed("tok"))
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Call(context.Background(), "query", map[string]any{"q": "pao"})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "found pao", res.Content[0].(*mcp.TextContent).Text)
	assert.Equal(t, int32(1), f.calls.Load())
}

func TestAToolTheConfigurationDoesNotNameIsNeverCalled(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), fixed("tok"))
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "secret", map[string]any{"q": "x"})
	assert.ErrorIs(t, err, ErrNotOffered)
	assert.Equal(t, int32(0), f.calls.Load())
}

func TestAResultPastTheCapIsRefusedNotCut(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), fixed("tok"))
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "big", map[string]any{"q": "all"})
	assert.ErrorIs(t, err, ErrTooLarge)
	assert.Equal(t, int32(1), f.calls.Load(), "it ran; its answer is what was refused")
}

func TestAWrongCredentialIsAnErrorInPaosWordsWithoutTheToken(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	s := New(conf(f.url), fixed("WRONG-SECRET"))
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
	s := New(conf(f.url), fixed("tok"))
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

	s := New(conf(ts.URL), nil)
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

// recorder is a server on another origin that records whether anything, or any
// credential, reached it.
type recorder struct {
	url  string
	hits atomic.Int32
	auth atomic.Value
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.auth.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		if a := req.Header.Get("Authorization"); a != "" {
			r.auth.Store(a)
		}
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	r.url = ts.URL
	return r
}

func TestARedirectToAnotherOriginIsNotFollowedAndGetsNoCredential(t *testing.T) {
	away := newRecorder(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, away.url+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ts.Close)
	s := New(conf(ts.URL), fixed("tok"))
	t.Cleanup(func() { _ = s.Close() })

	_, err := s.Call(context.Background(), "query", map[string]any{"q": "pao"})
	require.Error(t, err)
	assert.Equal(t, int32(0), away.hits.Load(), "the other origin is never asked, so neither the token nor the call reaches it")
	assert.Equal(t, "", away.auth.Load())
	assert.NotContains(t, err.Error(), "tok")
}

func TestARedirectWithinTheServersOwnOriginIsFollowed(t *testing.T) {
	f := newFakeUpstream(t, "tok")
	mux := http.NewServeMux()
	target, err := url.Parse(f.url)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
	})
	mux.Handle("/new", proxy)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s := New(conf(ts.URL+"/old"), fixed("tok"))
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Call(context.Background(), "query", map[string]any{"q": "pao"})
	require.NoError(t, err, "the server moving an endpoint within its own origin still works, credential and all")
	assert.Equal(t, "found pao", res.Content[0].(*mcp.TextContent).Text)
}

func TestTheTokenIsOnlyEverSentToTheConfiguredOrigin(t *testing.T) {
	away := newRecorder(t)
	b := &bearer{token: func() string { return "tok" }, header: "Authorization", prefix: "Bearer ", origin: originOf("https://mcp.example:443/x"), next: http.DefaultTransport}
	req, err := http.NewRequest(http.MethodGet, away.url, nil)
	require.NoError(t, err)
	resp, err := b.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, int32(1), away.hits.Load())
	assert.Equal(t, "", away.auth.Load(), "a request to another origin carries no credential, however it was made")

	for raw, want := range map[string]string{
		"https://MCP.example/a":      "https://mcp.example:443",
		"https://mcp.example:443/b":  "https://mcp.example:443",
		"http://mcp.example/c":       "http://mcp.example:80",
		"https://mcp.example:8443/d": "https://mcp.example:8443",
		"not a url":                  "",
	} {
		assert.Equal(t, want, originOf(raw), raw)
	}
}

// fixed reads a credential that never changes.
func fixed(v string) func() (string, error) { return func() (string, error) { return v, nil } }
