package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// tok holds characters JSON escapes, so its escaped form differs from it.
const tok = `s3cr"t\value`

// leakyUpstream is an MCP server that wants tok in header and sends it back in
// every place a server can: a tool's description and schema, a result, and an
// error.
func leakyUpstream(t *testing.T, header, want string) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "leaky", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string", "description": "key is " + tok}}}
	mcp.AddTool(srv, &mcp.Tool{Name: "query", Description: "uses " + tok, InputSchema: schema}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
		if a.Q == "fail" {
			return nil, nil, errors.New("bad key " + tok)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "your key " + tok + " works"}}}, map[string]any{"echo": tok}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(header) != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func leakyConf(url, header string) config.MCPUpstream {
	return config.MCPUpstream{Name: "leaky", URL: url, Secret: "key", Header: header, Tools: map[string]config.UpstreamTool{"query": {}}}
}

func TestTheSecretIsSentInTheHeaderTheServerNames(t *testing.T) {
	s := New(leakyConf(leakyUpstream(t, "X-Api-Key", tok), "X-Api-Key"), fixed(tok))
	t.Cleanup(func() { _ = s.Close() })
	_, err := s.Call(context.Background(), "query", map[string]any{"q": "x"})
	require.NoError(t, err, "a header of the server's own naming carries the secret as it is")

	s = New(leakyConf(leakyUpstream(t, "Authorization", "Bearer "+tok), ""), fixed(tok))
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "x"})
	require.NoError(t, err, "no header named is Authorization with Bearer")
}

func TestWhatTheServerSendsBackHoldsNoSecret(t *testing.T) {
	s := New(leakyConf(leakyUpstream(t, "X-Api-Key", tok), "X-Api-Key"), fixed(tok))
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	tools, _, err := s.Tools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.NotContains(t, tools[0].Description, tok)
	assert.Contains(t, tools[0].Description, "uses "+secretMarker)
	props := tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)
	assert.Equal(t, "key is "+secretMarker, props["q"].(map[string]any)["description"], "the schema too")

	res, err := s.Call(ctx, "query", map[string]any{"q": "x"})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Equal(t, "your key "+secretMarker+" works", text)
	assert.Equal(t, map[string]any{"echo": secretMarker}, res.StructuredContent, "inside JSON, where it reads escaped")

	res, err = s.Call(ctx, "query", map[string]any{"q": "fail"})
	if err == nil {
		require.True(t, res.IsError)
		assert.NotContains(t, res.Content[0].(*mcp.TextContent).Text, tok)
		assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, secretMarker, "a tool's error is scrubbed")
	} else {
		assert.NotContains(t, err.Error(), tok)
	}
}

func TestAnErrorOfPaosOwnHoldsNoSecret(t *testing.T) {
	s := New(leakyConf("http://127.0.0.1:1/"+tok, ""), fixed(tok))
	s.vmu.Lock()
	s.value = tok
	s.vmu.Unlock()
	err := s.fail("connect", errors.New("dial "+tok+" refused"))
	assert.NotContains(t, err.Error(), tok)
	assert.Contains(t, err.Error(), secretMarker)
}

func TestTheSecretIsReadAfreshOnEachConnection(t *testing.T) {
	var reads atomic.Int32
	current := atomic.Value{}
	current.Store(tok)
	url := leakyUpstream(t, "X-Api-Key", tok)
	s := New(leakyConf(url, "X-Api-Key"), func() (string, error) {
		reads.Add(1)
		return current.Load().(string) + "\n", nil
	})
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	_, err := s.Call(ctx, "query", map[string]any{"q": "x"})
	require.NoError(t, err, "a trailing newline, as a file holds it, is not part of the secret")
	_, err = s.Call(ctx, "query", map[string]any{"q": "x"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), reads.Load(), "one connection, one read")

	require.NoError(t, s.Close())
	current.Store("rotated")
	_, err = s.Call(ctx, "query", map[string]any{"q": "x"})
	require.Error(t, err, "the new connection presents the secret as it reads now")
	assert.Equal(t, int32(2), reads.Load())
}

func TestASecretThatCannotBeReadIsAnErrorWithoutAValue(t *testing.T) {
	s := New(leakyConf(leakyUpstream(t, "X-Api-Key", tok), "X-Api-Key"), func() (string, error) { return "", errors.New("file gone") })
	_, err := s.Call(context.Background(), "query", map[string]any{"q": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not read its credential: file gone")

	s = New(leakyConf(leakyUpstream(t, "X-Api-Key", tok), "X-Api-Key"), fixed("  \n"))
	_, err = s.Call(context.Background(), "query", map[string]any{"q": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential is empty")
}

// stdioServerArg marks the test binary re-run as a local MCP server.
const stdioServerArg = "upstream-test-stdio-server"

// TestStdioServerHelper is the local server, when the test binary is run as
// one: it offers "env", which reports the environment it was started with.
func TestStdioServerHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != stdioServerArg {
		t.Skip("run only as a local server")
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "local", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "env"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		var names []string
		for _, kv := range os.Environ() {
			names = append(names, strings.SplitN(kv, "=", 2)[0])
		}
		sort.Strings(names)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(names, ",") + " key=" + os.Getenv("LOCAL_KEY")}}}, nil, nil
	})
	_ = srv.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

func TestALocalServerGetsItsSecretInItsEnvironmentAndNothingElseOfPaos(t *testing.T) {
	t.Setenv("PAO_TOKEN", "operator-token")
	self, err := os.Executable()
	require.NoError(t, err)
	cfg := config.MCPUpstream{
		Name: "local", Command: []string{self, "-test.run=^TestStdioServerHelper$", stdioServerArg},
		Secret: "key", SecretEnv: "LOCAL_KEY", Tools: map[string]config.UpstreamTool{"env": {}},
	}
	s := New(cfg, fixed(tok))
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Call(context.Background(), "env", map[string]any{})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	names, key, _ := strings.Cut(text, " key=")
	for _, n := range strings.Split(names, ",") {
		assert.Contains(t, []string{"PATH", "HOME", "LANG", "LOCAL_KEY"}, n, "only what pao gives it")
	}
	assert.Contains(t, names, "LOCAL_KEY")
	assert.NotContains(t, text, "operator-token")
	assert.Equal(t, secretMarker, key, "it had the secret, and what it sends back is scrubbed")
}
