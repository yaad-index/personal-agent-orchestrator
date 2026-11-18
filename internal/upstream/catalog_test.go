package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// listedUpstream is an MCP server that counts how often its tools are listed and
// can be taken down and brought back.
type listedUpstream struct {
	url   string
	lists atomic.Int32
	down  atomic.Bool
}

func newListedUpstream(t *testing.T) *listedUpstream {
	t.Helper()
	u := &listedUpstream{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "query", Description: "Finds."}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + a.Q}}}, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m struct{ Method string }
		if json.Unmarshal(body, &m) == nil && m.Method == "tools/list" {
			u.lists.Add(1)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	u.url = ts.URL
	return u
}

func queryConf(name, url string) config.MCPUpstream {
	return config.MCPUpstream{Name: name, URL: url, Tools: map[string]config.UpstreamTool{"query": {Tags: []string{"research"}}}}
}

func names(ts []Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func TestACatalogListsEachServerOnceAndKeepsIt(t *testing.T) {
	a, b := newListedUpstream(t), newListedUpstream(t)
	c := NewCatalog(New(queryConf("alpha", a.url), ""), New(queryConf("beta", b.url), ""))
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	assert.Equal(t, []string{"alpha__query", "beta__query"}, names(c.Tools(ctx)), "every server, in configuration order")
	assert.Equal(t, []string{"alpha__query", "beta__query"}, names(c.Tools(ctx)))
	assert.Equal(t, int32(1), a.lists.Load(), "listed once")
	assert.Equal(t, int32(1), b.lists.Load())
}

func TestAServerThatCannotBeListedOffersNothingAndIsTriedAgainAfterTheRetryInterval(t *testing.T) {
	a, b := newListedUpstream(t), newListedUpstream(t)
	b.down.Store(true)
	c := NewCatalog(New(queryConf("alpha", a.url), ""), New(queryConf("beta", b.url), ""))
	t.Cleanup(func() { _ = c.Close() })
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	assert.Equal(t, []string{"alpha__query"}, names(c.Tools(ctx)), "the others are still offered")
	b.down.Store(false)
	now = now.Add(DefaultRetry - time.Second)
	assert.Equal(t, []string{"alpha__query"}, names(c.Tools(ctx)), "left alone inside the retry interval")
	assert.Zero(t, b.lists.Load())
	now = now.Add(2 * time.Second)
	assert.Equal(t, []string{"alpha__query", "beta__query"}, names(c.Tools(ctx)), "tried again after it")
}

func TestOneCallerGivingUpDoesNotSpoilTheListingForTheRest(t *testing.T) {
	a := newListedUpstream(t)
	c := NewCatalog(New(queryConf("alpha", a.url), ""))
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, []string{"alpha__query"}, names(c.Tools(ctx)))
}

func TestACatalogCallReachesTheToolOfThatPaoSideName(t *testing.T) {
	a := newListedUpstream(t)
	c := NewCatalog(New(queryConf("alpha", a.url), ""))
	t.Cleanup(func() { _ = c.Close() })
	res, err := c.Call(context.Background(), "alpha__query", json.RawMessage(`{"q":"x"}`))
	require.NoError(t, err)
	assert.Equal(t, "found x", res.Content[0].(*mcp.TextContent).Text)

	_, err = c.Call(context.Background(), "alpha__secret", nil)
	assert.ErrorIs(t, err, ErrNotOffered)
	_, err = c.Call(context.Background(), "query", nil)
	assert.ErrorIs(t, err, ErrNotOffered, "only by its pao-side name")
}

func TestACatalogUsedAtOnceListsEachServerOnce(t *testing.T) {
	a := newListedUpstream(t)
	c := NewCatalog(New(queryConf("alpha", a.url), ""))
	t.Cleanup(func() { _ = c.Close() })
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			assert.Equal(t, []string{"alpha__query"}, names(c.Tools(context.Background())))
		}()
		go func() {
			defer wg.Done()
			_, err := c.Call(context.Background(), "alpha__query", json.RawMessage(`{"q":"x"}`))
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), a.lists.Load())
}

// rawUpstream answers the MCP handshake and lists the tools given, as raw JSON,
// so it can offer what a well-behaved server library would refuse to.
func rawUpstream(t *testing.T, tools string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result string
		switch req.Method {
		case "initialize":
			v, _ := json.Marshal(req.Params.ProtocolVersion)
			result = `{"protocolVersion":` + string(v) + `,"capabilities":{"tools":{}},"serverInfo":{"name":"raw","version":"1"}}`
		case "tools/list":
			result = `{"tools":` + tools + `}`
		default:
			result = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + result + `}`))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestAToolWhoseInputIsNotAnObjectIsNotOffered(t *testing.T) {
	url := rawUpstream(t, `[
		{"name":"good","inputSchema":{"type":"object"}},
		{"name":"array","inputSchema":{"type":"array"}},
		{"name":"untyped","inputSchema":{}}
	]`)
	s := New(config.MCPUpstream{Name: "raw", URL: url, Tools: map[string]config.UpstreamTool{"good": {}, "array": {}, "untyped": {}}}, "")
	t.Cleanup(func() { _ = s.Close() })
	tools, missing, err := s.Tools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"raw__good"}, names(tools))
	assert.Equal(t, []string{"array", "untyped"}, missing)
}

func TestObjectSchema(t *testing.T) {
	assert.True(t, objectSchema(map[string]any{"type": "object"}))
	assert.True(t, objectSchema(json.RawMessage(`{"type":"object","properties":{}}`)))
	assert.False(t, objectSchema(map[string]any{"type": "string"}))
	assert.False(t, objectSchema(nil))
	assert.False(t, objectSchema("object"))
	assert.False(t, objectSchema(func() {}), "not even JSON")
}

func TestOfferedIsTheConfigurationAlone(t *testing.T) {
	got := Offered([]config.MCPUpstream{
		{Name: "beta", Tools: map[string]config.UpstreamTool{"b": {Risk: "read"}}},
		{Name: "alpha", Tools: map[string]config.UpstreamTool{"a": {Tags: []string{"x"}}}},
	})
	assert.Equal(t, []Tool{
		{Name: "alpha__a", Upstream: "alpha", Remote: "a", Tags: []string{"x"}},
		{Name: "beta__b", Upstream: "beta", Remote: "b", Risk: "read"},
	}, got)
}
