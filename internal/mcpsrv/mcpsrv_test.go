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

// fakeOps records who each operation was called as, and what with.
type fakeOps struct {
	agents  []AgentInfo
	recipes []RecipeInfo
	err     error

	spawnName string
	spawnErr  error
	kill      KillInfo
	killErr   error
	harvests  []HarvestInfo
	harvest   HarvestInfo
	file      []byte
	opErr     error
	sent      []Delivered
	sendErr   error

	calls *[]opCall
}

type opCall struct {
	op   string
	who  access.Subject
	args []string
}

func (f fakeOps) rec(op string, who access.Subject, args ...string) {
	if f.calls != nil {
		*f.calls = append(*f.calls, opCall{op, who, args})
	}
}

func (f fakeOps) Agents() []AgentInfo { return f.agents }
func (f fakeOps) Recipes() ([]RecipeInfo, error) {
	return f.recipes, f.err
}
func (f fakeOps) Spawn(_ context.Context, who access.Subject, recipe, name, channel string) (string, error) {
	f.rec("spawn", who, recipe, name, channel)
	return f.spawnName, f.spawnErr
}
func (f fakeOps) Kill(_ context.Context, who access.Subject, name string) (KillInfo, error) {
	f.rec("kill", who, name)
	return f.kill, f.killErr
}
func (f fakeOps) Harvests(who access.Subject) ([]HarvestInfo, error) {
	f.rec("harvests", who)
	return f.harvests, f.opErr
}
func (f fakeOps) Harvest(who access.Subject, id string) (HarvestInfo, error) {
	f.rec("harvest", who, id)
	return f.harvest, f.opErr
}
func (f fakeOps) Send(_ context.Context, who access.Subject, byTag bool, target, text string) ([]Delivered, error) {
	kind := "name"
	if byTag {
		kind = "tag"
	}
	f.rec("send", who, kind, target, text)
	return f.sent, f.sendErr
}
func (f fakeOps) HarvestFile(who access.Subject, id, path string) ([]byte, error) {
	f.rec("file", who, id, path)
	return f.file, f.opErr
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
		switch tool.Name {
		case "spawn_agent", "kill_agent", "send_message":
			assert.False(t, tool.Annotations.ReadOnlyHint, tool.Name)
		default:
			assert.True(t, tool.Annotations.ReadOnlyHint, "%s only reads", tool.Name)
		}
		assert.NotEmpty(t, tool.Description)
	}
	assert.ElementsMatch(t, []string{"list_agents", "list_recipes", "spawn_agent", "kill_agent", "list_harvests", "read_harvest", "send_message"}, got)
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

// toolText calls a tool and returns whether it failed, and its text.
func toolText(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return res.IsError, text.String()
}

func structured[Out any](t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) Out {
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

func TestSpawnAndKillRunAsTheResolvedSubject(t *testing.T) {
	var calls []opCall
	ops := fakeOps{spawnName: "tmp-a", kill: KillInfo{HarvestID: "tmp-a-1", Complete: true, Files: 2, Bytes: 9, Summary: "harvested 2"}, calls: &calls}
	url := serve(t, ops, Chain{NewOperatorResolver("op"), staticResolver{"agent-tok": access.Agent("boss")}})
	op, ag := connect(t, url, "op"), connect(t, url, "agent-tok")

	out := structured[spawnOutput](t, op, "spawn_agent", map[string]any{"recipe": "r", "name": "tmp-a", "channel": "local"})
	assert.Equal(t, "tmp-a", out.Name)
	k := structured[KillInfo](t, ag, "kill_agent", map[string]any{"name": "tmp-a"})
	assert.Equal(t, "tmp-a-1", k.HarvestID)
	assert.Equal(t, "harvested 2", k.Summary)
	_ = structured[spawnOutput](t, ag, "spawn_agent", map[string]any{"recipe": "r"})

	assert.Equal(t, []opCall{
		{"spawn", access.Operator, []string{"r", "tmp-a", "local"}},
		{"kill", access.Agent("boss"), []string{"tmp-a"}},
		{"spawn", access.Agent("boss"), []string{"r", "", ""}},
	}, calls, "each call reaches the operation as the subject its connection resolved to: the surface decides nothing, the operation does")
}

func TestARefusedSpawnOrKillIsAToolErrorThatSaysWhatTheOperationSaid(t *testing.T) {
	ops := fakeOps{spawnErr: errors.New("request denied: no thanks"), killErr: errors.New("no such temporary agent")}
	cs := connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")

	failed, text := toolText(t, cs, "spawn_agent", map[string]any{"recipe": "r"})
	assert.True(t, failed)
	assert.Contains(t, text, "no thanks", "a refusal by the gate reaches the caller with its reason")
	failed, text = toolText(t, cs, "kill_agent", map[string]any{"name": "x"})
	assert.True(t, failed)
	assert.Contains(t, text, "no such temporary agent")
}

func TestAnAgentThatIsNotReadyIsStillNamed(t *testing.T) {
	ops := fakeOps{spawnName: "tmp-a", spawnErr: errors.New("not ready")}
	cs := connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	failed, text := toolText(t, cs, "spawn_agent", map[string]any{"recipe": "r"})
	assert.True(t, failed)
	assert.Contains(t, text, `"tmp-a"`, "the name is how the agent is found to be inspected or killed")
}

func TestToolsRefuseMissingRequiredArguments(t *testing.T) {
	var calls []opCall
	cs := connect(t, serve(t, fakeOps{calls: &calls}, NewOperatorResolver("tok")), "tok")
	for tool, args := range map[string]map[string]any{
		"spawn_agent":  {"recipe": ""},
		"kill_agent":   {"name": ""},
		"read_harvest": {"id": ""},
	} {
		failed, _ := toolText(t, cs, tool, args)
		assert.True(t, failed, tool)
	}
	assert.Empty(t, calls, "an empty request never reaches the operation")
}

func TestHarvestListingAndReading(t *testing.T) {
	var calls []opCall
	ops := fakeOps{
		harvests: []HarvestInfo{{ID: "a-1", Agent: "a", Complete: true}},
		harvest:  HarvestInfo{ID: "a-1", Agent: "a", Files: []FileInfo{{Path: "x.txt", Size: 5}}},
		file:     []byte("hello"),
		calls:    &calls,
	}
	cs := connect(t, serve(t, ops, staticResolver{"a": access.Agent("boss")}), "a")

	list := structured[listHarvestsOutput](t, cs, "list_harvests", nil)
	require.Len(t, list.Harvests, 1)
	assert.Equal(t, "a-1", list.Harvests[0].ID)

	meta := structured[readHarvestOutput](t, cs, "read_harvest", map[string]any{"id": "a-1"})
	require.NotNil(t, meta.Harvest)
	assert.Equal(t, "x.txt", meta.Harvest.Files[0].Path)
	assert.Empty(t, meta.Content)

	body := structured[readHarvestOutput](t, cs, "read_harvest", map[string]any{"id": "a-1", "path": "x.txt"})
	assert.Equal(t, "utf8", body.Encoding)
	assert.Equal(t, "hello", body.Content)

	for _, c := range calls {
		assert.Equal(t, access.Agent("boss"), c.who, c.op)
	}
}

func TestBinaryAndOversizeHarvestFiles(t *testing.T) {
	ops := fakeOps{file: []byte{0xff, 0xfe, 0x00, 0x01}}
	cs := connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	out := structured[readHarvestOutput](t, cs, "read_harvest", map[string]any{"id": "a", "path": "bin"})
	assert.Equal(t, "base64", out.Encoding)
	assert.Equal(t, "//4AAQ==", out.Content)

	ops = fakeOps{file: make([]byte, maxFileBytes+1)}
	cs = connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	failed, text := toolText(t, cs, "read_harvest", map[string]any{"id": "a", "path": "big"})
	assert.True(t, failed)
	assert.Contains(t, text, "more than")

	ops = fakeOps{file: make([]byte, maxFileBytes)}
	cs = connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "read_harvest", Arguments: map[string]any{"id": "a", "path": "edge"}})
	require.NoError(t, err, "exactly the limit is allowed")
}

func TestAnEmptyHarvestListIsAnArray(t *testing.T) {
	cs := connect(t, serve(t, fakeOps{}, NewOperatorResolver("tok")), "tok")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_harvests"})
	require.NoError(t, err)
	raw, _ := json.Marshal(res.StructuredContent)
	assert.JSONEq(t, `{"harvests":[]}`, string(raw))
}

func TestAnOperationErrorOnReadIsAToolError(t *testing.T) {
	cs := connect(t, serve(t, fakeOps{opErr: errors.New("harvest not found")}, NewOperatorResolver("tok")), "tok")
	for tool, args := range map[string]map[string]any{
		"read_harvest": {"id": "x", "path": "p"},
	} {
		failed, text := toolText(t, cs, tool, args)
		assert.True(t, failed)
		assert.Contains(t, text, "harvest not found")
	}
	failed, _ := toolText(t, cs, "list_harvests", nil)
	assert.True(t, failed)
}

func TestSendMessageRunsAsTheResolvedSubjectAndNeverAsAnotherName(t *testing.T) {
	var calls []opCall
	ops := fakeOps{sent: []Delivered{{Agent: "tmp-a", Launched: true}}, calls: &calls}
	url := serve(t, ops, Chain{NewOperatorResolver("op"), staticResolver{"agent-tok": access.Agent("boss")}})
	op, ag := connect(t, url, "op"), connect(t, url, "agent-tok")

	out := structured[sendOutput](t, op, "send_message", map[string]any{"name": "tmp-a", "text": "hello"})
	require.Len(t, out.Delivered, 1)
	assert.True(t, out.Delivered[0].Launched)
	_ = structured[sendOutput](t, ag, "send_message", map[string]any{"tag": "team", "text": "all hands"})

	assert.Equal(t, []opCall{
		{"send", access.Operator, []string{"name", "tmp-a", "hello"}},
		{"send", access.Agent("boss"), []string{"tag", "team", "all hands"}},
	}, calls)

	// There is no way to say who the message is from: a caller that tries is refused.
	calls = nil
	res, err := ag.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"name": "x", "text": "hi", "sender": "operator"}})
	if err == nil {
		assert.True(t, res.IsError, "an unknown argument is refused")
	}
	assert.Empty(t, calls, "and nothing was sent")
}

func TestSendMessageNeedsExactlyOneTargetAndSomeText(t *testing.T) {
	var calls []opCall
	cs := connect(t, serve(t, fakeOps{calls: &calls}, NewOperatorResolver("tok")), "tok")
	for _, args := range []map[string]any{
		{"text": "hi"},
		{"name": "a", "tag": "b", "text": "hi"},
		{"name": "a", "text": ""},
		{"name": "a"},
	} {
		failed, _ := toolText(t, cs, "send_message", args)
		assert.True(t, failed, "%v", args)
	}
	assert.Empty(t, calls, "nothing malformed reaches the operation")
}

func TestSendMessageReportsPerAgentOutcomesAndRefusals(t *testing.T) {
	ops := fakeOps{sent: []Delivered{{Agent: "a"}, {Agent: "b", Error: "paste failed"}}}
	cs := connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	out := structured[sendOutput](t, cs, "send_message", map[string]any{"tag": "team", "text": "hi"})
	assert.Equal(t, ops.sent, out.Delivered, "one agent failing is reported without hiding the other")

	ops = fakeOps{sendErr: errors.New("no agent matches: tag \"team\"")}
	cs = connect(t, serve(t, ops, NewOperatorResolver("tok")), "tok")
	failed, text := toolText(t, cs, "send_message", map[string]any{"tag": "team", "text": "hi"})
	assert.True(t, failed)
	assert.Contains(t, text, "no agent matches")
}

type fakeTokens map[string]string

func (f fakeTokens) Resolve(token string) (string, bool) {
	n, ok := f[token]
	return n, ok
}

func TestAnAgentTokenResolvesToTheAgentOnlyWhileItsSessionIsAlive(t *testing.T) {
	alive := map[string]bool{"a": true}
	r := AgentResolver{Tokens: fakeTokens{"tok-a": "a", "tok-b": "b"}, Alive: func(n string) bool { return alive[n] }}

	s, ok := r.Resolve("tok-a")
	require.True(t, ok)
	assert.Equal(t, access.Agent("a"), s)

	_, ok = r.Resolve("tok-b")
	assert.False(t, ok, "its session is gone: the token no longer identifies anyone")
	_, ok = r.Resolve("unknown")
	assert.False(t, ok)
	alive["a"] = false
	_, ok = r.Resolve("tok-a")
	assert.False(t, ok, "and it stops the moment the session does, with nothing revoked")
}

func TestAnAgentResolverWithNothingToCheckRefusesEverything(t *testing.T) {
	_, ok := AgentResolver{Tokens: fakeTokens{"t": "a"}}.Resolve("t")
	assert.False(t, ok, "no liveness check means no identity: liveness is part of it, not an option")
	_, ok = AgentResolver{Alive: func(string) bool { return true }}.Resolve("t")
	assert.False(t, ok)
	_, ok = AgentResolver{Tokens: fakeTokens{"t": ""}, Alive: func(string) bool { return true }}.Resolve("t")
	assert.False(t, ok, "a token that names no agent identifies nobody")
}
