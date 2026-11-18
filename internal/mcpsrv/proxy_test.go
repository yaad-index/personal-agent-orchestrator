package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
