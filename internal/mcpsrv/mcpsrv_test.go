package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

type fakeOps struct {
	agents  []AgentInfo
	recipes []RecipeInfo
	err     error
}

func (f fakeOps) Agents() []AgentInfo { return f.agents }
func (f fakeOps) Recipes() ([]RecipeInfo, error) {
	return f.recipes, f.err
}

var sampleOps = fakeOps{
	agents: []AgentInfo{
		{Name: "boss", Tags: []string{"lead"}, Backend: "harness", State: "running"},
		{Name: "tmp-a", Tags: []string{"temp", "research"}, Backend: "harness", State: "stopped", Temporary: true, Recipe: "r", Channel: "local", Flag: "idle"},
	},
	recipes: []RecipeInfo{{Name: "researcher", Description: "reads and reports"}},
}

// staticResolver maps tokens to subjects.
type staticResolver map[string]access.Subject

func (r staticResolver) Resolve(token string) (access.Subject, bool) {
	s, ok := r[token]
	return s, ok
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func serve(t *testing.T, ops Operations, res Resolver, opts ...Option) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(ops, res, opts...).Handler())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call[Out any](t *testing.T, cs *mcp.ClientSession, tool string) Out {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out Out
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

type agentsOut struct {
	Agents []AgentInfo `json:"agents"`
}
type recipesOut struct {
	Recipes []RecipeInfo `json:"recipes"`
}

func names(as []AgentInfo) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func TestTheServerOffersItsTools(t *testing.T) {
	url := serve(t, sampleOps, NewOperatorResolver("tok"))
	cs := connect(t, url, "tok")
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, "%s only reads", tool.Name)
		assert.NotEmpty(t, tool.Description)
	}
	assert.ElementsMatch(t, []string{"list_agents", "list_recipes"}, got)
}

func TestTheOperatorSeesEverything(t *testing.T) {
	url := serve(t, sampleOps, NewOperatorResolver("tok"))
	cs := connect(t, url, "tok")

	agents := call[agentsOut](t, cs, "list_agents")
	assert.Equal(t, []string{"boss", "tmp-a"}, names(agents.Agents))
	tmp := agents.Agents[1]
	assert.True(t, tmp.Temporary)
	assert.Equal(t, "idle", tmp.Flag)
	assert.Equal(t, "local", tmp.Channel)
	assert.Equal(t, []string{"temp", "research"}, tmp.Tags)

	recipes := call[recipesOut](t, cs, "list_recipes")
	assert.Equal(t, sampleOps.recipes, recipes.Recipes)
}

func TestARequestWithoutAKnownTokenNeverReachesAnyTool(t *testing.T) {
	url := serve(t, sampleOps, NewOperatorResolver("tok"))
	for _, auth := range []string{"", "Bearer wrong", "Bearer ", "tok", "Basic dG9rOnRvaw=="} {
		req, err := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_agents"}}`))
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

func TestAnEmptyOperatorTokenResolvesNobody(t *testing.T) {
	_, ok := NewOperatorResolver("").Resolve("")
	assert.False(t, ok, "an unset token is not a credential that the empty string matches")
	_, ok = NewOperatorResolver("tok").Resolve("tok ")
	assert.False(t, ok)
	s, ok := NewOperatorResolver("tok").Resolve("tok")
	require.True(t, ok)
	assert.Equal(t, access.Operator, s)
}

func TestAnAgentSeesNothingByDefault(t *testing.T) {
	url := serve(t, sampleOps, staticResolver{"agent-tok": access.Agent("boss")})
	cs := connect(t, url, "agent-tok")

	assert.Empty(t, call[agentsOut](t, cs, "list_agents").Agents, "the default seam denies every agent, so none is listed: the same as a node with no agents")
	assert.Empty(t, call[recipesOut](t, cs, "list_recipes").Recipes)
}

// scopedReader lets one agent see agents carrying a tag, and the recipes.
type scopedReader struct{ agent, tag string }

func (r scopedReader) Allow(s access.Subject, a access.Action, res access.Resource) bool {
	if s.Kind == access.KindOperator {
		return true
	}
	if s.Kind != access.KindAgent || s.Name != r.agent {
		return false
	}
	switch a {
	case access.ListRecipes:
		return true
	case access.List:
		for _, tg := range res.Tags {
			if tg == r.tag {
				return true
			}
		}
	}
	return false
}

func TestAnAgentSeesExactlyWhatTheSeamAllowsIt(t *testing.T) {
	url := serve(t, sampleOps, staticResolver{"a": access.Agent("boss"), "b": access.Agent("other")},
		WithAuthorizer(scopedReader{agent: "boss", tag: "research"}))

	boss := connect(t, url, "a")
	assert.Equal(t, []string{"tmp-a"}, names(call[agentsOut](t, boss, "list_agents").Agents), "only the agent carrying the tag it may see")
	assert.Len(t, call[recipesOut](t, boss, "list_recipes").Recipes, 1)

	other := connect(t, url, "b")
	assert.Empty(t, call[agentsOut](t, other, "list_agents").Agents)
	assert.Empty(t, call[recipesOut](t, other, "list_recipes").Recipes)
}

func TestOneConnectionIsNeverTreatedAsAnotherSubject(t *testing.T) {
	url := serve(t, sampleOps, Chain{NewOperatorResolver("op"), staticResolver{"agent-tok": access.Agent("boss")}})
	op := connect(t, url, "op")
	ag := connect(t, url, "agent-tok")
	assert.Len(t, call[agentsOut](t, op, "list_agents").Agents, 2)
	assert.Empty(t, call[agentsOut](t, ag, "list_agents").Agents)
	assert.Len(t, call[agentsOut](t, op, "list_agents").Agents, 2, "and the agent's call did not change what the operator sees")
}

func TestAResolverThatReturnsNobodyIsRefused(t *testing.T) {
	url := serve(t, sampleOps, staticResolver{"zero": access.Subject{}})
	req, err := http.NewRequest("POST", url, strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer zero")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a token that resolves to no subject is not accepted")
}

func TestAToolErrorIsReportedToTheCaller(t *testing.T) {
	ops := sampleOps
	ops.err = errors.New("recipes unreadable")
	cs := connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_recipes"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestAnAgentThatLacksAnAuthorizerForRecipesGetsNoRecipeError(t *testing.T) {
	ops := sampleOps
	ops.err = errors.New("recipes unreadable")
	cs := connect(t, serve(t, ops, staticResolver{"a": access.Agent("boss")}), "a")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_recipes"})
	require.NoError(t, err)
	assert.False(t, res.IsError, "a denied caller is not told the recipes could not be read: the denial comes first")
}

func TestAnUnknownToolIsAProtocolError(t *testing.T) {
	cs := connect(t, serve(t, sampleOps, NewOperatorResolver("tok")), "tok")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
	require.Error(t, err)
}

func TestACallWithNoResolvedSubjectIsNobody(t *testing.T) {
	assert.Equal(t, access.Subject{}, subject(nil))
	assert.Equal(t, access.Subject{}, subject(&mcp.CallToolRequest{}))
	assert.Equal(t, access.Subject{}, subject(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}))
	assert.False(t, access.Allowed(nil, subject(&mcp.CallToolRequest{}), access.List, access.Resource{}), "and nobody is denied, so a missing identity never opens anything")
}

func TestTheServerKeepsNoSession(t *testing.T) {
	url := serve(t, sampleOps, NewOperatorResolver("tok"))
	req, err := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), "every request carries its own token; there is no session whose identity could be borrowed")
}
