package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/upstream"
)

var objectSchema = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}

// fakeProxy offers fixed tools and records every call it is asked to make.
type fakeProxy struct {
	tools []upstream.Tool
	err   error

	mu    sync.Mutex
	calls []proxyCall
}

type proxyCall struct {
	name string
	args string
}

func (p *fakeProxy) Tools(context.Context) []upstream.Tool { return p.tools }

func (p *fakeProxy) Call(_ context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, proxyCall{name, string(args)})
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "upstream says " + string(args)}}}, nil
}

func (p *fakeProxy) made() []proxyCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proxyCall{}, p.calls...)
}

func searchTools() []upstream.Tool {
	return []upstream.Tool{
		{Name: "search__query", Upstream: "search", Remote: "query", Description: "[from upstream search] Finds.", InputSchema: objectSchema, Tags: []string{"research"}, Risk: gate.RiskRead},
		{Name: "search__delete", Upstream: "search", Remote: "delete", Description: "[from upstream search] Deletes.", InputSchema: objectSchema, Tags: []string{"admin"}},
	}
}

// liveGrants is a grant source the test can change while the server runs.
type liveGrants struct {
	mu sync.Mutex
	gs []access.Grant
}

func (l *liveGrants) Of(s access.Subject) []access.Grant {
	l.mu.Lock()
	defer l.mu.Unlock()
	return grantList(l.gs).Of(s)
}

func (l *liveGrants) set(gs ...access.Grant) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gs = gs
}

// lockedGate is recordingGate, safe for calls made at once.
type lockedGate struct {
	allow bool
	mu    sync.Mutex
	asked []gate.Request
}

func (g *lockedGate) Decide(_ context.Context, req gate.Request) gate.Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, req)
	if g.allow {
		return gate.Decision{Allow: true}
	}
	return gate.Decision{Reason: "the operator said no"}
}

func (g *lockedGate) questions() []gate.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gate.Request{}, g.asked...)
}

var (
	bossResearch = access.Grant{Subject: access.Agent("boss"), Kind: access.ResourceTool, Tag: "research"}
	bossDelete   = access.Grant{Subject: access.Agent("boss"), Kind: access.ResourceTool, Name: "search__delete"}
	opResearch   = access.Grant{Subject: access.Operator, Kind: access.ResourceTool, Tag: "research"}
)

func proxyServer(t *testing.T, p Proxy, grants access.Grants, g Gate) string {
	t.Helper()
	return serve(t, sampleOps, staticResolver{"a": access.Agent("boss"), "b": access.Agent("other"), "op": access.Operator},
		WithAuthorizer(access.Matrix{Grants: grants}), WithProxy(p), WithGate(g))
}

func listed(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

func TestASubjectIsOfferedExactlyTheProxiedToolsItsAccessReaches(t *testing.T) {
	url := proxyServer(t, &fakeProxy{tools: searchTools()}, grantList{bossResearch, opResearch}, &lockedGate{allow: true})

	boss := listed(t, connect(t, url, "a"))
	require.Contains(t, boss, "search__query", "granted by tag")
	assert.NotContains(t, boss, "search__delete", "not granted: not listed")
	assert.Contains(t, boss, "list_agents", "pao's own tools are still there")
	assert.Equal(t, "[from upstream search] Finds.", boss["search__query"].Description)
	raw, err := json.Marshal(boss["search__query"].InputSchema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{"q":{"type":"string"}}}`, string(raw), "the server's input schema is passed on")

	other := listed(t, connect(t, url, "b"))
	assert.NotContains(t, other, "search__query", "with no grant, none")
	assert.NotContains(t, other, "search__delete")

	op := listed(t, connect(t, url, "op"))
	assert.Contains(t, op, "search__query", "the operator is offered what its grants reach")
	assert.NotContains(t, op, "search__delete", "and no more")
}

func TestAToolOutOfReachIsAnsweredExactlyAsOneThatDoesNotExist(t *testing.T) {
	p := &fakeProxy{tools: searchTools()}
	g := &lockedGate{allow: true}
	cs := connect(t, proxyServer(t, p, grantList{bossResearch}, g), "a")

	_, outOfReach := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search__delete", Arguments: map[string]any{"q": "x"}})
	_, absent := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search__nosuch", Arguments: map[string]any{"q": "x"}})
	require.Error(t, outOfReach)
	require.Error(t, absent)
	assert.Equal(t, strings.ReplaceAll(absent.Error(), "search__nosuch", "NAME"), strings.ReplaceAll(outOfReach.Error(), "search__delete", "NAME"))
	assert.Empty(t, p.made(), "nothing was forwarded")
	assert.Empty(t, g.questions(), "and nothing was asked")
}

func TestAProxiedCallIsJudgedAsPaosActionThenForwardedWithTheCallersArguments(t *testing.T) {
	p := &fakeProxy{tools: searchTools()}
	g := &lockedGate{allow: true}
	url := proxyServer(t, p, grantList{bossResearch, opResearch}, g)

	failed, text := toolText(t, connect(t, url, "a"), "search__query", map[string]any{"q": "x"})
	assert.False(t, failed, text)
	assert.Equal(t, `upstream says {"q":"x"}`, text, "the server's result is passed on")
	require.Equal(t, []proxyCall{{"search__query", `{"q":"x"}`}}, p.made())
	qs := g.questions()
	require.Len(t, qs, 1, "asked once")
	assert.Equal(t, gate.Request{Agent: "boss", Tool: "pao.search__query", Input: json.RawMessage(`{"q":"x"}`)}, qs[0])

	failed, _ = toolText(t, connect(t, url, "op"), "search__query", map[string]any{"q": "y"})
	assert.False(t, failed)
	assert.Len(t, g.questions(), 1, "the operator's own call is not asked of the operator")
	assert.Len(t, p.made(), 2)
}

func TestADeniedProxiedCallIsNeverForwarded(t *testing.T) {
	p := &fakeProxy{tools: searchTools()}
	cs := connect(t, proxyServer(t, p, grantList{bossResearch}, &lockedGate{}), "a")
	failed, text := toolText(t, cs, "search__query", map[string]any{"q": "x"})
	assert.True(t, failed)
	assert.Contains(t, text, "denied: the operator said no")
	assert.Empty(t, p.made())
}

func TestAnUpstreamFailureIsAToolErrorInTheProxysWords(t *testing.T) {
	p := &fakeProxy{tools: searchTools(), err: errors.New(`upstream "search" could not call query: gone`)}
	cs := connect(t, proxyServer(t, p, grantList{bossResearch}, &lockedGate{allow: true}), "a")
	failed, text := toolText(t, cs, "search__query", map[string]any{"q": "x"})
	assert.True(t, failed)
	assert.Equal(t, `upstream "search" could not call query: gone`, text)
}

func TestAGrantChangeIsSeenByTheNextRequest(t *testing.T) {
	grants := &liveGrants{}
	url := proxyServer(t, &fakeProxy{tools: searchTools()}, grants, &lockedGate{allow: true})
	cs := connect(t, url, "a")
	assert.NotContains(t, listed(t, cs), "search__delete")
	grants.set(bossDelete)
	assert.Contains(t, listed(t, cs), "search__delete", "granted: offered, on the same connection")
	failed, _ := toolText(t, cs, "search__delete", map[string]any{"q": "x"})
	assert.False(t, failed)
	grants.set()
	assert.NotContains(t, listed(t, cs), "search__delete", "revoked: no longer offered")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search__delete", Arguments: map[string]any{"q": "x"}})
	assert.Error(t, err, "revoked: gone at once")
}

func TestProxiedCallsMadeAtOnceAreEachJudgedForTheirOwnCaller(t *testing.T) {
	p := &fakeProxy{tools: searchTools()}
	g := &lockedGate{allow: true}
	grants := grantList{bossResearch, {Subject: access.Agent("other"), Kind: access.ResourceTool, Tag: "research"}}
	url := proxyServer(t, p, grants, g)
	sessions := map[string]*mcp.ClientSession{"boss": connect(t, url, "a"), "other": connect(t, url, "b")}

	const each = 10
	var wg sync.WaitGroup
	for who, cs := range sessions {
		for i := range each {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search__query", Arguments: map[string]any{"q": fmt.Sprintf("%s-%d", who, i)}})
				if assert.NoError(t, err) {
					assert.False(t, res.IsError)
				}
			}()
		}
	}
	wg.Wait()
	qs := g.questions()
	require.Len(t, qs, 2*each, "one question per call")
	for _, q := range qs {
		var in struct{ Q string }
		require.NoError(t, json.Unmarshal(q.Input, &in))
		assert.True(t, strings.HasPrefix(in.Q, q.Agent+"-"), "%s was asked about %s's call", q.Agent, in.Q)
	}
	assert.Len(t, p.made(), 2*each)
}

func TestWithoutAProxyOnlyPaosOwnToolsAreOffered(t *testing.T) {
	cs := connect(t, serve(t, sampleOps, NewOperatorResolver("tok"), WithAuthorizer(allowAllSeam{})), "tok")
	assert.Len(t, listed(t, cs), len(ToolNames))
}

func TestTheGateRulesReadAProxiedCallAtItsConfiguredRiskAndLetTheHookThrough(t *testing.T) {
	p, err := gate.LoadPolicy(filepath.Join(t.TempDir(), "policy.json"), GateRules("pao", searchTools()...)...)
	require.NoError(t, err)
	g := &gate.Gate{Policy: p, AutoAllow: gate.DefaultAutoAllow()}
	decide := func(tool string) bool {
		return g.Decide(context.Background(), gate.Request{Agent: "boss", Tool: tool, Input: []byte(`{"q":"x"}`)}).Allow
	}
	assert.True(t, decide("pao.search__query"), "read risk: no question")
	assert.False(t, decide("pao.search__delete"), "no risk: unclassified, so asked; with no one to ask, denied")
	assert.True(t, decide("mcp__pao__search__query"), "the hook lets the call through to pao")
	assert.True(t, decide("mcp__pao__search__delete"), "whatever its risk: pao judges it")
	assert.False(t, decide("search__query"), "the bare pao-side name is no rule")
	assert.False(t, decide("mcp__other__search__query"), "nor is another server's")

	rules := GateRules("pao", searchTools()...)
	n := 0
	for _, r := range rules {
		if r.Tool == "pao.search__delete" {
			n++
		}
	}
	assert.Zero(t, n, "a tool with no risk brings no risk rule")
}

func TestAGrantAddedIsAnnouncedToASessionAlreadyOpen(t *testing.T) {
	grants := &liveGrants{}
	grants.set(bossResearch)
	srv := New(sampleOps, staticResolver{"a": access.Agent("boss"), "b": access.Agent("other")},
		WithAuthorizer(access.Matrix{Grants: grants}), WithProxy(&fakeProxy{tools: searchTools()}), WithGate(&lockedGate{allow: true}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// A client that keeps the stream a server's notifications arrive on and
	// reports each tool-list change, at a protocol version where the server sends
	// every change unasked, as to the coding agents pao runs today.
	open := func(token string) (*mcp.ClientSession, <-chan struct{}) {
		changed := make(chan struct{}, 8)
		c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
		})
		cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1,
		}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close() })
		return cs, changed
	}
	boss, bossChanged := open("a")
	_, otherChanged := open("b")
	before := listed(t, boss)
	require.Contains(t, before, "search__query")
	assert.NotContains(t, before, "search__delete")

	// heard reports whether a notification arrives within the wait.
	heard := func(ch <-chan struct{}, wait time.Duration) bool {
		select {
		case <-ch:
			return true
		case <-time.After(wait):
			return false
		}
	}
	// Let anything already on its way arrive, so what follows is about the
	// refresh alone.
	for heard(bossChanged, 500*time.Millisecond) || heard(otherChanged, 0) {
	}

	// Nothing changed: nobody is told anything.
	srv.Refresh(context.Background())
	assert.False(t, heard(bossChanged, time.Second), "a refresh that changes nothing announces nothing")

	// The grant is added, and the session hears of it with no request of its own.
	grants.set(bossResearch, bossDelete)
	srv.Refresh(context.Background())
	select {
	case <-bossChanged:
	case <-time.After(5 * time.Second):
		t.Fatal("the open session was not told its tools changed")
	}
	assert.False(t, heard(otherChanged, 500*time.Millisecond), "a subject whose tools did not change is not told")
	assert.Contains(t, listed(t, boss), "search__delete")
}

func TestACallToAToolRevokedSinceItWasOfferedIsAnsweredAsUnknown(t *testing.T) {
	p := &fakeProxy{tools: searchTools()}
	g := &lockedGate{allow: true}
	srv := New(sampleOps, staticResolver{}, WithAuthorizer(access.Matrix{Grants: grantList{}}), WithProxy(p), WithGate(g))
	// The tool was offered to boss, and boss's grant went before the call ran.
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "search__delete", Arguments: json.RawMessage(`{"q":"x"}`)},
		Extra:  &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{subjectKey: access.Agent("boss")}}},
	}
	res, err := srv.proxied(searchTools()[1])(context.Background(), req)
	assert.Nil(t, res)
	var werr *jsonrpc.Error
	require.ErrorAs(t, err, &werr)
	assert.Equal(t, int64(jsonrpc.CodeInvalidParams), werr.Code)
	assert.Equal(t, `unknown tool "search__delete"`, werr.Message, "the server's own words for a tool that does not exist")
	assert.Empty(t, p.made(), "nothing was forwarded")
	assert.Empty(t, g.questions(), "and nothing was asked")
}
