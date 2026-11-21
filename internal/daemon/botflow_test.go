package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// service is a stand-in for the chat service the bot adapter talks to.
type service struct {
	mu      sync.Mutex
	pending []map[string]any
	sent    []map[string]any
	nextID  int
	// other is every other call, by method.
	other map[string][]map[string]any
}

func (s *service) calls(method string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.other[method]...)
}

func (s *service) push(update map[string]any) {
	s.mu.Lock()
	s.pending = append(s.pending, update)
	s.mu.Unlock()
}

func (s *service) texts(chat float64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m["chat_id"].(float64) == chat {
			out = append(out, m["text"].(string))
		}
	}
	return out
}

func (s *service) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := map[string]any{}
	if err := r.ParseMultipartForm(1 << 20); err == nil {
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
			if n, err := strconv.ParseFloat(v[0], 64); err == nil && k == "chat_id" {
				body[k] = n
			}
		}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		out := s.pending
		s.pending = nil
		raw, _ := json.Marshal(out)
		if len(out) == 0 {
			s.mu.Unlock()
			time.Sleep(15 * time.Millisecond)
			s.mu.Lock()
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, raw)
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		s.sent = append(s.sent, body)
		s.nextID++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, 5000+s.nextID)
	case strings.HasSuffix(r.URL.Path, "/editMessageText"), strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if s.other == nil {
			s.other = map[string][]map[string]any{}
		}
		s.other[method] = append(s.other[method], body)
		if method == "answerCallbackQuery" {
			_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%v}}`, body["message_id"])
	default:
		http.NotFound(w, r)
	}
}

// sentWith returns the first message sent to chat whose text contains want.
func (s *service) sentWith(chat float64, want string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.sent {
		if m["chat_id"].(float64) == chat && strings.Contains(m["text"].(string), want) {
			return m
		}
	}
	return nil
}

func pressUpdate(id, msg, from int, qid, data string) map[string]any {
	return map[string]any{"update_id": id, "callback_query": map[string]any{
		"id": qid, "data": data,
		"from":    map[string]any{"id": from, "is_bot": false},
		"message": map[string]any{"message_id": msg, "date": 1700000000, "chat": map[string]any{"id": from, "type": "private"}},
	}}
}

func chatUpdate(id, msg, from int, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{
		"message_id": msg, "text": text,
		"from": map[string]any{"id": from, "is_bot": false},
		"chat": map[string]any{"id": from, "type": "private"},
	}}
}

// TestAHumanReachesAnAgentThroughTheBotAndHearsBack runs the whole chain on a real
// node: a stranger is recorded and told nothing, an invitation registers a person, a
// registered person who is not flagged is refused, a flagged one reaches the agent
// attributed by subject, and the agent's output comes back to that person's chat.
func TestAHumanReachesAnAgentThroughTheBotAndHearsBack(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	const person = 4242

	svc.push(chatUpdate(1, 10, person, "/pao bee hello"))
	require.Eventually(t, func() bool {
		rej, err := n.client.Rejected(ctx)
		return err == nil && len(rej) == 1
	}, 5*time.Second, 20*time.Millisecond, "a stranger's message is recorded")
	assert.Empty(t, svc.texts(person), "and answered with nothing")

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(2, 11, person, "/register "+inv.Secret))
	require.Eventually(t, func() bool { return len(svc.texts(person)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "registered.", svc.texts(person)[0])

	svc.push(chatUpdate(3, 12, person, "/pao bee hello"))
	require.Eventually(t, func() bool { return len(svc.texts(person)) == 2 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "no access.", svc.texts(person)[1], "registered is not the same as allowed")

	require.NoError(t, n.client.SetOperator(ctx, "alice", true))
	svc.push(chatUpdate(4, 13, person, "/pao agent bee hello again"))
	require.Eventually(t, func() bool {
		for _, s := range svc.texts(person) {
			if strings.Contains(s, "echo:") {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the agent's output reaches the person's chat")
	var out string
	for _, s := range svc.texts(person) {
		if strings.Contains(s, "echo:") {
			out = s
		}
	}
	assert.True(t, strings.HasPrefix(out, "[bee]: "), "pao's output reads as a line from the agent: %q", out)
	assert.Contains(t, out, "hello again")
	assert.Contains(t, out, "[alice]", "the agent saw the subject's name, not the external id")
	assert.NotContains(t, out, "4242")

	assert.Empty(t, os.Getenv("PAO_TEST_BOT_TOKEN"), "the credential is not in the environment agents inherit")
}

// lineWith waits for a line sent to the chat that contains want, and returns it.
func lineWith(t *testing.T, svc *service, chat float64, want string) string {
	t.Helper()
	var out string
	require.Eventually(t, func() bool {
		for _, s := range svc.texts(chat) {
			if strings.Contains(s, want) {
				out = s
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "a line containing %q", want)
	return out
}

func TestTheChannelsDefaultAgentIsReloadedSwitchedAtRuntimeAndSpeaksUnprefixed(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")

	n := startNodeWith(t, map[string]string{"bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
		// What a node that ran before left behind.
		require.NoError(t, os.MkdirAll(cfg.Orchestrator.StateDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, defaultsFile),
			[]byte(`{"version":3,"defaults":{"chat":"bee"}}`), 0o600))
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	const person = 4242

	d, err := n.client.Defaults(ctx)
	require.NoError(t, err)
	assert.Equal(t, local.DefaultsJSON{Version: 3, Defaults: []local.DefaultJSON{{Channel: "chat", Agent: "bee"}}}, d, "reloaded at start")

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, person, "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "first unaddressed"))
	out := lineWith(t, svc, person, "first unaddressed")
	assert.True(t, strings.HasPrefix(out, "echo:"), "the default answers with no name before it: %q", out)

	ch, err := n.client.SetDefault(ctx, "chat", "wasp")
	require.NoError(t, err)
	assert.Equal(t, local.DefaultChangeJSON{Channel: "chat", Agent: "wasp", Previous: "bee"}, ch)
	svc.push(chatUpdate(3, 12, person, "second unaddressed"))
	out = lineWith(t, svc, person, "second unaddressed")
	assert.True(t, strings.HasPrefix(out, "echo:"), "the new default answers, unprefixed: %q", out)

	svc.push(chatUpdate(4, 13, person, "/pao bee third addressed"))
	out = lineWith(t, svc, person, "third addressed")
	assert.True(t, strings.HasPrefix(out, "[bee]: "), "an agent that is not the default is named: %q", out)

	// A leading @name is no address any more (ADR-0026 point 9): the message goes
	// where the sender last addressed, as the text it was sent as.
	svc.push(chatUpdate(5, 14, person, "@wasp fourth"))
	out = lineWith(t, svc, person, "@wasp fourth")
	assert.Equal(t, "[bee]: echo: [alice]: @wasp fourth", out)

	// A command is answered to the sender and reaches no agent (ADR-0026 points 1 and 5).
	svc.push(chatUpdate(6, 15, person, "/pao help"))
	out = lineWith(t, svc, person, "pao commands:")
	assert.Contains(t, out, "/pao stream <agent>")
	svc.push(chatUpdate(7, 16, person, "/pao stream bee"))
	lineWith(t, svc, person, "bee's output goes to chat only")
	svc.push(chatUpdate(8, 17, person, "/pao stream nobody"))
	lineWith(t, svc, person, `no agent "nobody"`)
	svc.push(chatUpdate(9, 18, person, "/pao agents"))
	out = lineWith(t, svc, person, "agents you may reach:")
	assert.Equal(t, "agents you may reach:\nbee: running; tags team\nwasp: running; tags team", out)
	require.NoError(t, n.client.AgentAction(ctx, "wasp", "stop"))
	svc.push(chatUpdate(10, 19, person, "/pao agents"))
	out = lineWith(t, svc, person, "wasp: stopped")
	assert.Equal(t, "agents you may reach:\nbee: running; tags team\nwasp: stopped; tags team", out, "the state is read when asked")
	for _, s := range svc.texts(person) {
		assert.NotContains(t, s, "echo: [alice]: /pao", "no command reached an agent")
	}

	_, err = n.client.SetDefault(ctx, "local", "bee")
	assert.ErrorContains(t, err, "404: no chat channel", "the local channel takes no chat inbound")
	_, err = n.client.SetDefault(ctx, "chat", "nobody")
	assert.ErrorContains(t, err, `404: no agent "nobody"`)

	ch, err = n.client.ClearDefault(ctx, "chat")
	require.NoError(t, err)
	assert.Equal(t, local.DefaultChangeJSON{Channel: "chat", Previous: "wasp"}, ch)
	d, err = n.client.Defaults(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), d.Version)
	assert.Empty(t, d.Defaults)
}

// TestAGateQuestionIsAnsweredByAButtonAndItsButtonsAreClosed runs ADR-0027 on a
// real node: the gate's question reaches the operator's chat with three buttons,
// a press decides it as the typed command would and is answered to the presser,
// and the question's buttons are then replaced by the outcome. A press by someone
// who is not the operator decides nothing.
func TestAGateQuestionIsAnsweredByAButtonAndItsButtonsAreClosed(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	const person, other = 4242, 5151

	for i, who := range []struct {
		name string
		id   int
	}{{"alice", person}, {"bob", other}} {
		require.NoError(t, n.client.AddSubject(ctx, who.name))
		inv, err := n.client.Invite(ctx, who.name, time.Hour)
		require.NoError(t, err)
		svc.push(chatUpdate(1+i, 10+i, who.id, "/register "+inv.Secret))
		lineWith(t, svc, float64(who.id), "registered.")
	}
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	decided := make(chan gate.Decision, 1)
	go func() {
		d, _ := gate.Ask(ctx, n.gateSock, gate.Request{Agent: "bee", Tool: "Bash", Input: []byte(`{"command":"echo hi"}`)})
		decided <- d
	}()
	lineWith(t, svc, person, `agent "bee" wants to run Bash`)
	q := svc.sentWith(person, `agent "bee" wants to run Bash`)
	assert.JSONEq(t, `{"inline_keyboard":[[`+
		`{"text":"ALLOW","callback_data":"/pao allow q1"},`+
		`{"text":"ALWAYS","callback_data":"/pao always q1"},`+
		`{"text":"DENY","callback_data":"/pao deny q1"}]]}`, q["reply_markup"].(string))
	assert.Nil(t, svc.sentWith(other, "wants to run"), "pao's output goes to the operator only")
	native := int(5000 + len(svc.texts(person)) + len(svc.texts(other)))

	svc.push(pressUpdate(20, native, other, "cb-other", "/pao allow q1"))
	require.Eventually(t, func() bool { return len(svc.calls("answerCallbackQuery")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "no access.", svc.calls("answerCallbackQuery")[0]["text"], "a press is checked as its command, from the presser")
	select {
	case d := <-decided:
		t.Fatalf("a press by someone who is not the operator decided the question: %+v", d)
	case <-time.After(100 * time.Millisecond):
	}

	svc.push(pressUpdate(21, native, person, "cb-op", "/pao allow q1"))
	select {
	case d := <-decided:
		assert.Equal(t, gate.Decision{Allow: true, Via: "operator", Reason: "approved by the operator"}, d)
	case <-time.After(5 * time.Second):
		t.Fatal("the operator's press did not decide the question")
	}
	require.Eventually(t, func() bool { return len(svc.calls("answerCallbackQuery")) == 2 }, 5*time.Second, 20*time.Millisecond)
	a := svc.calls("answerCallbackQuery")[1]
	assert.Equal(t, "cb-op", a["callback_query_id"])
	assert.Equal(t, "q1: allow", a["text"], "the press is answered to the presser")

	require.Eventually(t, func() bool { return len(svc.calls("editMessageText")) == 1 }, 5*time.Second, 20*time.Millisecond)
	e := svc.calls("editMessageText")[0]
	assert.Equal(t, float64(person), e["chat_id"])
	assert.Equal(t, strconv.Itoa(native), e["message_id"])
	assert.Equal(t, q["text"].(string)+"\n\n— allowed", e["text"])
	assert.JSONEq(t, `{"inline_keyboard":[]}`, e["reply_markup"].(string), "the buttons are gone")

	svc.push(pressUpdate(22, native, person, "cb-late", "/pao deny q1"))
	require.Eventually(t, func() bool { return len(svc.calls("answerCallbackQuery")) == 3 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, svc.calls("answerCallbackQuery")[2]["text"], "no such pending question", "a stale press decides nothing")
	assert.Len(t, svc.calls("editMessageText"), 1)
}

func groupUpdate(id, msg, from, chat int, text string) map[string]any {
	u := chatUpdate(id, msg, from, text)
	u["message"].(map[string]any)["chat"] = map[string]any{"id": chat, "type": "supergroup"}
	return u
}

// TestAnAdmittedGroupServesTheOperatorAndNoOneElse runs ADR-0028 on a real node:
// the operator reaches an agent from an admitted group and its output comes back to
// the group, not privately; anyone else in the group, a group that is not admitted,
// and a registration from a group are recorded and answered with nothing; a gate
// question reaches the operator privately and not the group, and a press decides
// it and closes its buttons.
func TestAnAdmittedGroupServesTheOperatorAndNoOneElse(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, other, stranger, group, unlisted = 4242, 5151, 6161, -700, -800

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	for i, who := range []struct {
		name string
		id   int
	}{{"alice", person}, {"bob", other}} {
		require.NoError(t, n.client.AddSubject(ctx, who.name))
		inv, err := n.client.Invite(ctx, who.name, time.Hour)
		require.NoError(t, err)
		svc.push(chatUpdate(1+i, 10+i, who.id, "/register "+inv.Secret))
		lineWith(t, svc, float64(who.id), "registered.")
	}
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(groupUpdate(3, 20, person, group, "/pao agent bee hello group"))
	out := lineWith(t, svc, group, "hello group")
	assert.Equal(t, "[bee]: echo: [alice]: hello group", out, "attributed to the operator's subject, answered in the group")
	assert.Nil(t, svc.sentWith(person, "hello group"), "the output goes to the group, not privately")

	svc.push(groupUpdate(4, 21, person, group, "and again"))
	assert.Equal(t, "[bee]: echo: [alice]: and again", lineWith(t, svc, group, "and again"), "the address made in the group is kept there")

	// A reply goes back to the conversation it was addressed from, though the
	// channel has an admitted group (ADR-0029 point 2).
	svc.push(chatUpdate(40, 40, person, "/pao agent bee in private"))
	assert.Equal(t, "[bee]: echo: [alice]: in private", lineWith(t, svc, person, "in private"), "a private message is answered privately")
	assert.Nil(t, svc.sentWith(group, "in private"), "and not in the group")
	svc.push(groupUpdate(41, 41, person, group, "back in the group"))
	lineWith(t, svc, group, "back in the group")
	assert.Nil(t, svc.sentWith(person, "back in the group"), "the next message from the group is answered there")

	inv, err := n.client.Invite(ctx, "bob", time.Hour)
	require.NoError(t, err)
	svc.push(groupUpdate(5, 22, other, group, "/pao agent bee from bob"))
	svc.push(groupUpdate(6, 23, person, unlisted, "/pao agent bee from elsewhere"))
	svc.push(groupUpdate(7, 24, stranger, group, "/register "+inv.Secret))
	// The record keeps each sender's latest rejection.
	want := map[string]string{strconv.Itoa(other): "message/outside access in group team", strconv.Itoa(person): "message/group " + strconv.Itoa(unlisted), strconv.Itoa(stranger): "registration/from a group"}
	require.Eventually(t, func() bool {
		rej, err := n.client.Rejected(ctx)
		if err != nil {
			return false
		}
		got := map[string]string{}
		for _, e := range rej {
			got[e.Sender] = string(e.Kind) + "/" + e.Reason
		}
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond, "each refusal is recorded")
	assert.Nil(t, svc.sentWith(group, "from bob"), "a member the policy does not admit is not routed")
	assert.Nil(t, svc.sentWith(unlisted, "elsewhere"), "a group that is not admitted is not served")
	assert.Empty(t, svc.texts(unlisted), "and nothing is said there")
	assert.Empty(t, svc.texts(stranger), "a registration from a group is not answered")
	for _, s := range svc.texts(group) {
		assert.NotContains(t, s, "registered", "nor answered in the group")
		assert.NotContains(t, s, "no access", "a refusal is never said in the group")
	}

	decided := make(chan gate.Decision, 1)
	go func() {
		d, _ := gate.Ask(ctx, n.gateSock, gate.Request{Agent: "bee", Tool: "Bash", Input: []byte(`{"command":"echo hi"}`)})
		decided <- d
	}()
	// A gate question goes to the operator privately, not to the group (ADR-0029
	// point 4).
	lineWith(t, svc, person, `agent "bee" wants to run Bash`)
	q := svc.sentWith(person, `agent "bee" wants to run Bash`)
	assert.Contains(t, q["reply_markup"].(string), "/pao allow q1", "the question carries its buttons")
	assert.Nil(t, svc.sentWith(group, "wants to run"), "not in the group")
	assert.Nil(t, svc.sentWith(other, "wants to run"), "never to anyone but the operator")

	native := func() int {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		for i, m := range svc.sent {
			if m["chat_id"].(float64) == person && strings.Contains(m["text"].(string), "wants to run") {
				return 5001 + i
			}
		}
		return 0
	}()
	require.NotZero(t, native)
	svc.push(pressUpdate(30, native, person, "cb-private", "/pao allow q1"))
	select {
	case d := <-decided:
		assert.Equal(t, gate.Decision{Allow: true, Via: "operator", Reason: "approved by the operator"}, d)
	case <-time.After(5 * time.Second):
		t.Fatal("the operator's press did not decide the question")
	}
	require.Eventually(t, func() bool { return len(svc.calls("editMessageText")) == 1 }, 5*time.Second, 20*time.Millisecond, "the copy is closed")
	assert.Equal(t, float64(person), svc.calls("editMessageText")[0]["chat_id"].(float64))
}

// TestAStandingBindingFollowsTheAgentBesideTheOrigin runs ADR-0029 point 3 on a
// real node: the operator writes privately, the reply comes back privately, and a
// standing binding shows the agent's output in the group as well, with its
// prefix in place of the agent's name.
func TestAStandingBindingFollowsTheAgentBesideTheOrigin(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
		cfg.Orchestrator.Bindings = []config.Binding{{Agent: "bee", Stream: config.StreamStdout, To: "group:team", Level: 1, Prefix: "B:"}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "/pao agent bee hello"))
	assert.Equal(t, "[bee]: echo: [alice]: hello", lineWith(t, svc, person, "hello"), "the origin: privately, named as before")
	assert.Equal(t, "B: echo: [alice]: hello", lineWith(t, svc, group, "hello"), "the binding: in the group, with its prefix")
}

// TestALogBindingMirrorsWhatTheAgentIsSent runs the log level on a real node: the
// group follows the agent at log, so it is shown the operator's private message,
// named by its sender, and then the agent's answer.
func TestALogBindingMirrorsWhatTheAgentIsSent(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
		cfg.Orchestrator.Bindings = []config.Binding{{Agent: "bee", Stream: config.StreamStdout, To: "group:team", Level: config.BindingLevel(core.LevelLog)}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "/pao agent bee hello"))
	assert.Equal(t, "[bee]: echo: [alice]: hello", lineWith(t, svc, person, "echo"), "the origin is answered")
	assert.Equal(t, "[alice]: hello", lineWith(t, svc, group, "[alice]: hello"), "the group is shown what the agent was sent")
	assert.Equal(t, "[bee]: echo: [alice]: hello", lineWith(t, svc, group, "echo"), "and what it said")
	for _, s := range svc.texts(person) {
		assert.NotEqual(t, "[alice]: hello", s, "the sender is not shown their own message")
	}
}

// TestAStderrBindingTakesTheAgentsGateQuestions runs ADR-0029 point 4 on a real
// node: the agent's stderr is bound to the group, so its gate question is put to
// the group and not to the operator privately, and the operator's press there
// decides it and closes the group's copy.
func TestAStderrBindingTakesTheAgentsGateQuestions(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
		cfg.Orchestrator.Bindings = []config.Binding{{Agent: "bee", Stream: config.StreamStderr, To: "group:team"}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	decided := make(chan gate.Decision, 1)
	go func() {
		d, _ := gate.Ask(ctx, n.gateSock, gate.Request{Agent: "bee", Tool: "Bash", Input: []byte(`{"command":"echo hi"}`)})
		decided <- d
	}()
	lineWith(t, svc, group, `agent "bee" wants to run Bash`)
	q := svc.sentWith(group, `agent "bee" wants to run Bash`)
	assert.Contains(t, q["reply_markup"].(string), "/pao allow q1", "the question carries its buttons")
	assert.Nil(t, svc.sentWith(person, "wants to run"), "not privately: the binding takes the default's place")

	native := func() int {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		for i, m := range svc.sent {
			if m["chat_id"].(float64) == group && strings.Contains(m["text"].(string), "wants to run") {
				return 5001 + i
			}
		}
		return 0
	}()
	require.NotZero(t, native)
	press := pressUpdate(30, native, person, "cb-group", "/pao allow q1")
	press["callback_query"].(map[string]any)["message"].(map[string]any)["chat"] = map[string]any{"id": group, "type": "supergroup"}
	svc.push(press)
	select {
	case d := <-decided:
		assert.Equal(t, gate.Decision{Allow: true, Via: "operator", Reason: "approved by the operator"}, d)
	case <-time.After(5 * time.Second):
		t.Fatal("the operator's press in the group did not decide the question")
	}
	require.Eventually(t, func() bool { return len(svc.calls("editMessageText")) == 1 }, 5*time.Second, 20*time.Millisecond, "the group's copy is closed")
	assert.Equal(t, float64(group), svc.calls("editMessageText")[0]["chat_id"].(float64))
}

// TestANoticeThatAnAgentIsNotReadyGoesToItsStderrDestinations runs ADR-0029
// point 4 for a notice about an agent: bee's stderr is bound to the group, so the
// notice that it did not become ready goes there; wasp has no stderr binding, so
// its notice goes to the operator privately and not to the admitted group.
func TestANoticeThatAnAgentIsNotReadyGoesToItsStderrDestinations(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": neverReady, "wasp": neverReady}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
		cfg.Orchestrator.Bindings = []config.Binding{{Agent: "bee", Stream: config.StreamStderr, To: "group:team"}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "/pao agent bee hello"))
	lineWith(t, svc, group, `agent "bee" is not ready`)
	assert.Nil(t, svc.sentWith(person, `agent "bee" is not ready`), "bee's notice is not shown privately")

	svc.push(chatUpdate(3, 12, person, "/pao agent wasp hello"))
	lineWith(t, svc, person, `agent "wasp" is not ready`)
	assert.Nil(t, svc.sentWith(group, `agent "wasp" is not ready`), "wasp's notice is not shown in the group")
}

// neverReady starts an agent that stays up and never shows its ready prompt; what
// pao appends to the command is taken as the shell's own arguments.
const neverReady = "sh -c 'sleep 30' sh"

// TestABindingSetWhileTheNodeRunsFollowsTheAgent runs ADR-0029 point 8 over the
// local API on a real node: a binding set while the node runs shows the agent's
// output in the group with its prefix, is listed beside the config's, and once
// removed shows nothing more. A config binding cannot be removed, and an agent the
// operator may not reach is answered as one that does not exist.
func TestABindingSetWhileTheNodeRunsFollowsTheAgent(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": "", "hidden": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
		cfg.Orchestrator.Bindings = []config.Binding{{Agent: "bee", Stream: config.StreamStderr, To: "private:alice"}}
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == "hidden" {
				cfg.Profiles[i].Tags = []string{"elsewhere"}
			}
		}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	b, err := n.client.BindStanding(ctx, local.StandingJSON{Agent: "bee", To: "group:team", Prefix: "B:"})
	require.NoError(t, err)
	assert.Equal(t, local.StandingJSON{Agent: "bee", Stream: "stdout", To: "group:team", Level: "1", Prefix: "B:", From: "runtime"}, b, "defaults filled in")

	list, err := n.client.StandingBindings(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []local.StandingJSON{
		{Agent: "bee", Stream: "stderr", To: "private:alice", From: "config"},
		{Agent: "bee", Stream: "stdout", To: "group:team", Level: "1", Prefix: "B:", From: "runtime"},
	}, list.Bindings)

	svc.push(chatUpdate(2, 11, person, "/pao agent bee hello"))
	assert.Equal(t, "[bee]: echo: [alice]: hello", lineWith(t, svc, person, "hello"), "the origin")
	assert.Equal(t, "B: echo: [alice]: hello", lineWith(t, svc, group, "hello"), "the binding set at runtime")

	_, err = n.client.UnbindStanding(ctx, local.StandingJSON{Agent: "bee", To: "group:team"})
	require.NoError(t, err)
	svc.push(chatUpdate(3, 12, person, "/pao agent bee again"))
	lineWith(t, svc, person, "again")
	assert.Nil(t, svc.sentWith(group, "again"), "removed, it shows nothing more")

	_, err = n.client.UnbindStanding(ctx, local.StandingJSON{Agent: "bee", Stream: "stderr", To: "private:alice"})
	requireStatus(t, err, "409", "a config binding is the config's")
	_, err = n.client.BindStanding(ctx, local.StandingJSON{Agent: "bee", To: "private:nobody"})
	requireStatus(t, err, "400", "no such subject")
	_, err = n.client.BindStanding(ctx, local.StandingJSON{Agent: "bee", To: "group:team", Level: "loud"})
	requireStatus(t, err, "400", "a level is a number or log")

	_, err = n.client.BindStanding(ctx, local.StandingJSON{Agent: "hidden", To: "group:team"})
	requireStatus(t, err, "404", "an agent out of the operator's reach is not there")
	_, err = n.client.StandingBindings(ctx, "hidden")
	requireStatus(t, err, "404", "nor are its bindings")
}

// requireStatus checks that a local client call failed with the HTTP status.
func requireStatus(t *testing.T, err error, status, msg string) {
	t.Helper()
	require.Error(t, err, msg)
	assert.True(t, strings.HasPrefix(err.Error(), status+":"), "%s: %v", msg, err)
}

// TestTheOperatorBindsAnAgentFromChat runs ADR-0029 point 8 from chat on a real
// node: /pao bind, /pao bindings and /pao unbind are answered to the operator
// alone, and the binding shows the agent's output in the group while it lasts.
func TestTheOperatorBindsAnAgentFromChat(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "/pao bind bee group:team 2 B:"))
	assert.Equal(t, `group:team follows bee's stdout at level 2, prefix "B:"`, lineWith(t, svc, person, "group:team follows bee"))
	svc.push(chatUpdate(3, 12, person, "/pao bind bee private:alice stderr"))
	assert.Equal(t, "private:alice follows bee's stderr", lineWith(t, svc, person, "private:alice follows bee"))
	svc.push(chatUpdate(4, 13, person, "/pao bindings"))
	assert.Equal(t, "standing bindings:\n"+`group:team follows bee's stdout at level 2, prefix "B:" (runtime)`+"\nprivate:alice follows bee's stderr (runtime)", lineWith(t, svc, person, "standing bindings"))
	svc.push(chatUpdate(5, 14, person, "/pao unbind bee private:alice stderr"))
	lineWith(t, svc, person, "private:alice no longer follows bee's stderr")

	svc.push(chatUpdate(6, 15, person, "/pao agent bee hello"))
	assert.Equal(t, "B: echo: [alice]: hello", lineWith(t, svc, group, "hello"), "the binding set from chat")

	svc.push(chatUpdate(7, 16, person, "/pao unbind bee group:team"))
	lineWith(t, svc, person, "group:team no longer follows bee's stdout")
	svc.push(chatUpdate(8, 17, person, "/pao agent bee again"))
	lineWith(t, svc, person, "again")
	assert.Nil(t, svc.sentWith(group, "again"), "unbound, the group is shown nothing more")

	svc.push(chatUpdate(9, 18, person, "/pao bind bee group:nope"))
	assert.Equal(t, `no admitted group is named "nope"`, lineWith(t, svc, person, "nope"))
	for _, s := range svc.texts(group) {
		assert.NotContains(t, s, "follows", "a command's answer goes to the sender alone")
		assert.NotContains(t, s, "standing bindings")
	}
}

func TestTheOperatorSpawnsAndKillsATemporaryAgentFromChat(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(goodRecipe("fake"), "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(groupUpdate(2, 11, person, group, "/pao spawn fake scout"))
	assert.Equal(t, "spawning an agent from recipe fake; you will be told when it is ready", lineWith(t, svc, group, "spawning"))
	assert.Equal(t, "[pao]: scout is ready, spawned from recipe fake", lineWith(t, svc, person, "scout is ready"), "told privately, though asked in the group")
	assert.Nil(t, svc.sentWith(group, "scout is ready"))
	a, ok := agentByName(t, n, "scout")
	require.True(t, ok)
	assert.Equal(t, "chat", a.Channel, "its stream is bound to the channel the spawn came from")

	svc.push(chatUpdate(3, 12, person, "/pao agent scout hi"))
	lineWith(t, svc, person, "echo: [alice]: hi")

	svc.push(chatUpdate(4, 13, person, "/pao kill bee"))
	assert.Equal(t, "no such temporary agent", lineWith(t, svc, person, "no such temporary agent"), "a configured agent is not killed from chat")

	svc.push(chatUpdate(5, 14, person, "/pao kill scout"))
	killed := lineWith(t, svc, person, "scout is killed")
	assert.Regexp(t, `^scout is killed; its outputs are harvested as \S+\nharvested \d+ file\(s\), \d+ bytes, from "scout" as \S+\n`, killed, "with the report, in the one answer")
	_, ok = agentByName(t, n, "scout")
	assert.False(t, ok)

	svc.push(chatUpdate(6, 15, person, "/pao spawn nope"))
	lineWith(t, svc, person, "spawn from recipe nope failed: ")
}

func TestAGovernorRequestFromChatIsAnsweredToTheRequesterAlone(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": "", "gov": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Governor = config.Governor{Agent: "gov"}
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(groupUpdate(2, 11, person, group, "/gpao pause the bee"))
	assert.Equal(t, "[gov]: echo: [alice]: pause the bee", lineWith(t, svc, person, "pause the bee"), "the governor's answer, privately")
	assert.Nil(t, svc.sentWith(group, "pause the bee"), "never in the group it was asked in")

	svc.push(chatUpdate(3, 12, person, "/gpao list the agents"))
	lineWith(t, svc, person, "echo: [alice]: list the agents")

	svc.push(chatUpdate(4, 13, person, "/pao stream gov"))
	assert.Equal(t, `bad request: agent "gov" is the governor: what it says goes only to whoever asked it`, lineWith(t, svc, person, "is the governor"))
	svc.push(chatUpdate(5, 14, person, "/pao bind gov group:team"))
	refusal := `agent "gov" is the governor: what it says goes only to whoever asked it`
	require.Eventually(t, func() bool {
		for _, s := range svc.texts(person) {
			if s == refusal {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the bind is refused in the operation's words")
}

func TestAPressOfACommandWithAListIsAnsweredInThePressersPrivateChat(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person = 4242
	n := startNodeWith(t, map[string]string{"bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, float64(person), "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(pressUpdate(2, 77, person, "cb-1", "/pao agents"))
	assert.Equal(t, "agents you may reach:\nbee: stopped; tags team\nwasp: stopped; tags team", lineWith(t, svc, person, "agents you may reach"),
		"the command ran as the presser, and its whole answer is a message in the presser's chat")
	require.Eventually(t, func() bool { return len(svc.calls("answerCallbackQuery")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "done: the answer is in your private chat with the bot", svc.calls("answerCallbackQuery")[0]["text"])
}

func TestANoticeAboutTheGovernorGoesToItsRequesterAlone(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const alice, bob, group = 4242, 4343, -700

	n := startNodeWith(t, map[string]string{"gov": neverReady}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Governor = config.Governor{Agent: "gov"}
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups:  []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	for i, who := range []struct {
		name string
		id   int
	}{{"alice", alice}, {"bob", bob}} {
		require.NoError(t, n.client.AddSubject(ctx, who.name))
		inv, err := n.client.Invite(ctx, who.name, time.Hour)
		require.NoError(t, err)
		svc.push(chatUpdate(1+i, 10+i, who.id, "/register "+inv.Secret))
		lineWith(t, svc, float64(who.id), "registered.")
		require.NoError(t, n.client.SetOperator(ctx, who.name, true))
	}

	svc.push(groupUpdate(3, 12, alice, group, "/gpao pause the bee"))
	lineWith(t, svc, alice, `agent "gov" is not ready`)
	assert.Nil(t, svc.sentWith(bob, `agent "gov" is not ready`), "the other operator is not shown it")
	assert.Nil(t, svc.sentWith(group, `agent "gov" is not ready`), "nor the group it was asked in")
}
