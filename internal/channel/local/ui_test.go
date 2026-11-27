package local

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/webui"
)

// uiServer is a local server with the management UI mounted, and a signed-in
// session's cookie.
func uiServer(t *testing.T, l Layer) (string, *http.Cookie) {
	t.Helper()
	sessions := webui.NewSessions()
	node := NodeJSON{Node: "n1", Build: "abc", Agents: []NodeAgentJSON{{Name: "a", Tags: []string{"home"}, Backend: "claude-code", State: "running"}}}
	_, ts := newServer(t, l, WithSessions(sessions), WithMount(webui.Prefix, webui.Handler(sessions, token)), WithNode(func() NodeJSON { return node }))
	req, err := http.NewRequest("POST", ts.URL+"/ui/api/login", strings.NewReader(`{"token":"`+token+`"}`))
	require.NoError(t, err)
	req.Header.Set(webui.Header, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	return ts.URL, resp.Cookies()[0]
}

func send(t *testing.T, method, url, body string, header map[string]string, c *http.Cookie) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestAUISessionIsTheOperatorsAndAChangeMustComeFromItsPage(t *testing.T) {
	l := &fakeLayer{}
	url, c := uiServer(t, l)
	ui := map[string]string{webui.Header: "1"}

	assert.Equal(t, http.StatusOK, send(t, "GET", url+"/v1/node", "", nil, c), "a read needs only the session")
	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", url+"/v1/node", "", nil, nil))

	msg := `{"sender":"human","text":"hi","target":{"kind":"name","value":"a"}}`
	assert.Equal(t, http.StatusForbidden, send(t, "POST", url+"/v1/messages", msg, nil, c), "the cookie alone changes nothing")
	assert.Equal(t, http.StatusForbidden, send(t, "POST", url+"/v1/messages", msg, map[string]string{webui.Header: "1", "Origin": "http://evil.example"}, c), "nor from another site's page")
	assert.Empty(t, l.inbound)
	assert.Equal(t, http.StatusOK, send(t, "POST", url+"/v1/messages", msg, ui, c), "from the UI's page it does")
	assert.Len(t, l.inbound, 1)

	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", url+"/v1/node", "", map[string]string{"Authorization": "Bearer wrong"}, c),
		"a wrong token is wrong even with a session: the two are not mixed")
	assert.Equal(t, http.StatusOK, send(t, "GET", url+"/v1/node", "", map[string]string{"Authorization": "Bearer " + token}, nil), "the token works as before")
}

func TestWithoutSessionsACookieIsNothing(t *testing.T) {
	sessions := webui.NewSessions()
	_, ts := newServer(t, &fakeLayer{}, WithMount(webui.Prefix, webui.Handler(sessions, token)), WithNode(func() NodeJSON { return NodeJSON{} }))
	req, err := http.NewRequest("POST", ts.URL+"/ui/api/login", strings.NewReader(`{"token":"`+token+`"}`))
	require.NoError(t, err)
	req.Header.Set(webui.Header, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", ts.URL+"/v1/node", "", nil, resp.Cookies()[0]), "only a server given the sessions takes them")
}

func TestTheNodeIsReadWithItsAgents(t *testing.T) {
	node := NodeJSON{Node: "n1", Build: "abc", Agents: []NodeAgentJSON{{Name: "a", Tags: []string{"home"}, Backend: "claude-code", State: "running", Temporary: true}}}
	_, ts := newServer(t, &fakeLayer{}, WithNode(func() NodeJSON { return node }))
	got, err := (&Client{URL: ts.URL, Token: token}).Node(context.Background())
	require.NoError(t, err)
	assert.Equal(t, node, got)
	resp := do(t, ts, "GET", "/v1/node", "", bearer())
	var raw map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	assert.Equal(t, []any{"agents", "build", "node"}, keys(raw))

	_, plain := newServer(t, &fakeLayer{})
	assert.Equal(t, http.StatusNotFound, do(t, plain, "GET", "/v1/node", "", bearer()).StatusCode, "absent unless the node gives it")
}

func keys(m map[string]any) []any {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	res := make([]any, len(out))
	for i, k := range out {
		res[i] = k
	}
	return res
}

func TestARestartIsAskedOnlyByTheOperator(t *testing.T) {
	asked := 0
	sessions := webui.NewSessions()
	_, ts := newServer(t, &fakeLayer{}, WithRestart(func() { asked++ }), WithSessions(sessions), WithMount(webui.Prefix, webui.Handler(sessions, token)))
	assert.Equal(t, http.StatusUnauthorized, send(t, "POST", ts.URL+"/v1/node/restart", "", nil, nil))
	assert.Equal(t, 0, asked)
	require.NoError(t, (&Client{URL: ts.URL, Token: token}).RestartNode(context.Background()))
	assert.Equal(t, 1, asked)

	req, err := http.NewRequest("POST", ts.URL+"/ui/api/login", strings.NewReader(`{"token":"`+token+`"}`))
	require.NoError(t, err)
	req.Header.Set(webui.Header, "1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	c := resp.Cookies()[0]
	assert.Equal(t, http.StatusForbidden, send(t, "POST", ts.URL+"/v1/node/restart", "", nil, c), "a page elsewhere cannot restart pao")
	assert.Equal(t, http.StatusAccepted, send(t, "POST", ts.URL+"/v1/node/restart", "", map[string]string{webui.Header: "1"}, c))
	assert.Equal(t, 2, asked)
}

func TestWaitingQuestionsAndRecipesAreReadByTheOperator(t *testing.T) {
	asked := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	qs := []QuestionJSON{{ID: "q1", Agent: "a", Tool: "Bash", Text: "agent \"a\" wants to run Bash", Asked: asked}}
	rs := []RecipeJSON{{Name: "fake", Description: "a fake agent", Tags: []string{"temp"}}}
	_, ts := newServer(t, &fakeLayer{}, WithQuestions(func() []QuestionJSON { return qs }), WithRecipes(func() ([]RecipeJSON, error) { return rs, nil }))
	c := &Client{URL: ts.URL, Token: token}
	gotQ, err := c.Questions(context.Background())
	require.NoError(t, err)
	assert.Equal(t, qs, gotQ)
	gotR, err := c.Recipes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, rs, gotR)
	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", ts.URL+"/v1/questions", "", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", ts.URL+"/v1/recipes", "", nil, nil))

	_, plain := newServer(t, &fakeLayer{})
	assert.Equal(t, http.StatusNotFound, do(t, plain, "GET", "/v1/questions", "", bearer()).StatusCode)
}

func TestTheChannelsAreReadByTheOperator(t *testing.T) {
	cs := []ChannelJSON{{Name: "local", Kind: "local", Groups: []string{}}, {Name: "tg", Kind: "telegram", Groups: []string{"family"}}}
	_, ts := newServer(t, &fakeLayer{}, WithChannels(cs))
	got, err := (&Client{URL: ts.URL, Token: token}).Channels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, cs, got)
	assert.Equal(t, http.StatusUnauthorized, send(t, "GET", ts.URL+"/v1/channels", "", nil, nil))
}

type fakeChannelConfig struct {
	put     []ChannelConfigJSON
	deleted []string
	err     error
}

func (f *fakeChannelConfig) List(context.Context) (ChannelConfigListJSON, error) {
	return ChannelConfigListJSON{ManagedFile: "/n/managed.yaml", Channels: []ChannelConfigEntryJSON{}, Kinds: []string{"bot"}, Secrets: []string{}}, nil
}
func (f *fakeChannelConfig) Put(_ context.Context, c ChannelConfigJSON) error {
	f.put = append(f.put, c)
	return f.err
}
func (f *fakeChannelConfig) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return f.err
}

func TestTheChannelConfigurationIsTheOperatorsToChange(t *testing.T) {
	f := &fakeChannelConfig{}
	_, ts := newServer(t, &fakeLayer{}, WithChannelConfig(f))
	c := &Client{URL: ts.URL, Token: token}
	ctx := context.Background()
	l, err := c.ChannelConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "/n/managed.yaml", l.ManagedFile)
	require.NoError(t, c.PutChannelConfig(ctx, ChannelConfigJSON{ID: 2, Name: "ui", Kind: "bot", CredentialSecret: "s"}))
	require.NoError(t, c.DeleteChannelConfig(ctx, "ui"))
	assert.Equal(t, "s", f.put[0].CredentialSecret)
	assert.Equal(t, []string{"ui"}, f.deleted)

	assert.Equal(t, http.StatusBadRequest, do(t, ts, "PUT", "/v1/config/channels/other", `{"id":2,"name":"ui","kind":"bot"}`, bearer()).StatusCode, "the name is the path's")
	assert.Equal(t, http.StatusUnauthorized, send(t, "PUT", ts.URL+"/v1/config/channels/ui", `{"name":"ui"}`, nil, nil))
	assert.Len(t, f.put, 1)

	f.err = ErrConflict
	assert.Equal(t, http.StatusConflict, do(t, ts, "PUT", "/v1/config/channels/ui", `{"id":2,"name":"ui","kind":"bot"}`, bearer()).StatusCode)
	f.err = ErrBadRequest
	assert.Equal(t, http.StatusBadRequest, do(t, ts, "DELETE", "/v1/config/channels/ui", "", bearer()).StatusCode)
}

func TestARestartThatWouldNotStartIsRefused(t *testing.T) {
	asked := 0
	why := errors.New("pao would not start: channel \"x\": variable X is not set")
	_, ts := newServer(t, &fakeLayer{}, WithRestart(func() { asked++ }), WithRestartCheck(func() error { return why }))
	resp := do(t, ts, "POST", "/v1/node/restart", "", bearer())
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, string(body), `variable X is not set`)
	assert.Zero(t, asked, "pao keeps running as it is")
	why = nil
	require.NoError(t, (&Client{URL: ts.URL, Token: token}).RestartNode(context.Background()))
	assert.Equal(t, 1, asked)
}
