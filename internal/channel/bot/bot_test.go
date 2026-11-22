package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

const tok = "123:TOK-SECRET" // the service issues "<bot id>:<secret>"

// update and message are the service's JSON shapes, as far as the fake needs them.
type update struct {
	ID       int64     `json:"update_id"`
	Message  *message  `json:"message,omitempty"`
	Callback *callback `json:"callback_query,omitempty"`
}

// callback is a press of a button under a message.
type callback struct {
	ID   string `json:"id"`
	From struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type message struct {
	ID int64 `json:"message_id"`
	// Date is when the message was sent. The service gives a message a bot can no
	// longer reach a date of 0.
	Date int64 `json:"date,omitempty"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from,omitempty"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text    string `json:"text"`
	ReplyTo *struct {
		ID int64 `json:"message_id"`
	} `json:"reply_to_message,omitempty"`
	// Thread is the thread the service puts the message in, and Topic its mark
	// that the message was written in a topic of a group split into topics.
	Thread int  `json:"message_thread_id,omitempty"`
	Topic  bool `json:"is_topic_message,omitempty"`
	// The service's reports about a group.
	NewMembers  []member `json:"new_chat_members,omitempty"`
	MigrateTo   int64    `json:"migrate_to_chat_id,omitempty"`
	MigrateFrom int64    `json:"migrate_from_chat_id,omitempty"`
}

type member struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

// server is a fake of the service's API: it hands out queued batches of updates,
// records what is sent, and can be made to fail.
type server struct {
	t  *testing.T
	ts *httptest.Server

	mu       sync.Mutex
	batches  [][]update
	polls    []map[string]string
	sent     []map[string]string
	edits    []map[string]string
	answers  []map[string]string
	failEdit bool
	nextID   int64
	failNow  int              // polls to fail before serving
	failSend map[float64]bool // chat ids whose sends fail
	// mode shapes the answer to a poll: "" is fine, "okfalse" is a 200 with ok false, "status" a 500 with ok true.
	mode string
	// describe, when set, is put in a failure's description.
	describe string
	// huge makes a send answer with more than the adapter reads.
	huge bool
}

func newServer(t *testing.T) *server {
	s := &server{t: t, nextID: 100, failSend: map[float64]bool{}}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *server) endpoint() string { return s.ts.URL + "/bot{token}/{method}" }

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/bot"+tok+"/") {
		http.Error(w, "bad path", 404)
		return
	}
	body := map[string]string{}
	if err := r.ParseMultipartForm(1 << 20); err == nil {
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
		}
	}
	switch strings.TrimPrefix(r.URL.Path, "/bot"+tok+"/") {
	case "getUpdates":
		s.polls = append(s.polls, body)
		if s.failNow > 0 {
			s.failNow--
			w.WriteHeader(500)
			_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":500,"description":"boom %s"}`, s.describe)
			return
		}
		switch s.mode {
		case "okfalse":
			_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, s.describe)
			return
		}
		var batch []update
		if len(s.batches) > 0 {
			batch, s.batches = s.batches[0], s.batches[1:]
		} else {
			s.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			s.mu.Lock()
		}
		raw, _ := json.Marshal(batch)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, raw)
	case "sendMessage":
		chat, _ := strconv.ParseFloat(body["chat_id"], 64)
		if s.failSend[chat] {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"blocked"}`)
			return
		}
		switch s.mode {
		case "okfalse":
			_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, s.describe)
			return
		case "unknown":
			w.WriteHeader(502)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":502,"description":"gateway"}`)
			return
		}
		if s.huge {
			_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":1,"text":"%s"}}`, strings.Repeat("x", maxResponse))
			return
		}
		s.sent = append(s.sent, body)
		s.nextID++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, s.nextID)
	case "editMessageText":
		if s.failEdit {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"message to edit not found"}`)
			return
		}
		s.edits = append(s.edits, body)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%s}}`, body["message_id"])
	case "answerCallbackQuery":
		s.answers = append(s.answers, body)
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	default:
		http.Error(w, "nope", 404)
	}
}

func (s *server) snapshot(which *[]map[string]string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string(nil), (*which)...)
}

// press is a press by from of a button carrying data, under message msg in chat.
func press(id int64, qid string, from, chat int64, chatType string, msg int64, data string) update {
	c := &callback{ID: qid, Data: data}
	c.From.ID = from
	if msg != 0 {
		m := &message{ID: msg, Date: 1700000000}
		m.Chat.ID, m.Chat.Type = chat, chatType
		c.Message = m
	}
	return update{ID: id, Callback: c}
}

func (s *server) queue(us ...update) { s.mu.Lock(); s.batches = append(s.batches, us); s.mu.Unlock() }

func (s *server) sentTo(chat float64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if c, _ := strconv.ParseFloat(m["chat_id"], 64); c == chat {
			out = append(out, m["text"])
		}
	}
	return out
}

func priv(id, msg int64, from int64, text string) update {
	m := &message{ID: msg, Text: text}
	m.From = &struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}{ID: from}
	m.Chat.ID, m.Chat.Type = from, "private"
	return update{ID: id, Message: m}
}

// handler is the layer as the adapter sees it.
type handler struct {
	mu         sync.Mutex
	got        []comm.ChannelInbound
	res        comm.ChannelResult
	err        error
	recipients []string
	defaults   map[string]string // channel to default agent
	// slowFirst holds the first inbound back before it is taken in.
	slowFirst time.Duration
	calls     int
}

func (h *handler) ReceiveFromChannel(_ context.Context, in comm.ChannelInbound) (comm.ChannelResult, error) {
	h.mu.Lock()
	h.calls++
	first := h.calls == 1
	h.mu.Unlock()
	if first && h.slowFirst > 0 {
		time.Sleep(h.slowFirst)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, in)
	res := h.res
	// As the layer does: a command that addresses an agent or a tag says so.
	if res.Delivery != nil && command.Is(in.Text) {
		res.Addressed = command.Run(context.Background(), nil, command.Caller{Operator: true}, in.Text).Address
	}
	return res, h.err
}
func (h *handler) Destinations(string, core.Message) []string { return h.recipients }
func (h *handler) NativeOf(string, string) (string, bool)     { return "", false }
func (h *handler) DefaultAgent(channel string) (string, bool) {
	a, ok := h.defaults[channel]
	return a, ok
}

// DefaultAgentIn is a conversation's own default, keyed "channel/conversation" in
// defaults, and otherwise the channel's.
func (h *handler) DefaultAgentIn(channel, conversation string) (string, bool) {
	if a, ok := h.defaults[channel+"/"+conversation]; ok {
		return a, true
	}
	return h.DefaultAgent(channel)
}
func (h *handler) inbound() []comm.ChannelInbound {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]comm.ChannelInbound(nil), h.got...)
}

func spec(s *server, opts map[string]any) adapter.Spec {
	o := map[string]any{"endpoint": s.endpoint(), "poll_timeout": 0}
	for k, v := range opts {
		o[k] = v
	}
	return adapter.Spec{ID: 1, Name: "chat", Kind: "bot", Credential: tok, Options: o}
}

func start(t *testing.T, s *server, h *handler, opts map[string]any) (*Bot, context.CancelFunc) {
	t.Helper()
	b, err := newBot(spec(s, opts), h)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})
	return b, cancel
}

func waitFor(t *testing.T, f func() bool, msg string) {
	t.Helper()
	require.Eventually(t, f, 5*time.Second, 5*time.Millisecond, msg)
}

func TestNewNeedsAnEndpointWithTokenAndMethodAndKnowsItsOptions(t *testing.T) {
	good := map[string]any{"endpoint": "https://x.example/bot{token}/{method}"}
	for name, c := range map[string]struct {
		opts map[string]any
		want string
	}{
		"none":            {nil, "endpoint"},
		"no token":        {map[string]any{"endpoint": "https://x/{method}"}, "endpoint"},
		"no method":       {map[string]any{"endpoint": "https://x/{token}"}, "endpoint"},
		"other path":      {map[string]any{"endpoint": "https://x/{token}/{method}"}, "endpoint"},
		"no placeholders": {map[string]any{"endpoint": "https://x.example/plain"}, "ending in"},
		"token in server": {map[string]any{"endpoint": "https://x/{token}/bot{token}/{method}"}, "endpoint"},
		"not a url":       {map[string]any{"endpoint": "ftp://x/bot{token}/{method}"}, "http or https"},
		"unknown":         {map[string]any{"endpoint": good["endpoint"], "nope": 1}, `unknown option "nope"`},
		"poll too long":   {map[string]any{"endpoint": good["endpoint"], "poll_timeout": 600}, "poll_timeout"},
		"poll negative":   {map[string]any{"endpoint": good["endpoint"], "poll_timeout": -1}, "poll_timeout"},
		"poll not number": {map[string]any{"endpoint": good["endpoint"], "poll_timeout": "x"}, "whole number"},
		"poll fraction":   {map[string]any{"endpoint": good["endpoint"], "poll_timeout": 1.5}, "whole number"},
		"limit small":     {map[string]any{"endpoint": good["endpoint"], "message_limit": 10}, "message_limit"},
	} {
		_, err := New(adapter.Spec{Name: "chat", Credential: tok, Options: c.opts}, &handler{})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
		assert.Equal(t, err, CheckOptions(c.opts), "the check without a credential fails the same way: %s", name)
	}
	assert.NoError(t, CheckOptions(good))
	_, err := New(adapter.Spec{Name: "chat", Options: good}, &handler{})
	require.ErrorContains(t, err, "credential", "no credential")
	a, err := New(adapter.Spec{Name: "chat", Credential: tok, Options: map[string]any{"endpoint": good["endpoint"], "poll_timeout": 5.0, "message_limit": uint64(500)}}, &handler{})
	require.NoError(t, err)
	assert.Equal(t, "chat", a.Name())
	assert.Equal(t, 500, a.(*Bot).limit)
	assert.Equal(t, 5*time.Second, a.(*Bot).poll)
}

func TestAPrivateMessageReachesTheLayerWithTheServicesSenderIdAndNativeIds(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(1, 55, 900, "/pao bee hello there"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")

	in := h.inbound()[0]
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "900", Native: "900:55", Text: "/pao bee hello there", Raw: "/pao bee hello there"}, in,
		"a command goes to the layer whole: the layer reads it")
}

// grp is priv said in group conversation chat.
func grp(id, msg, from, chat int64, text string) update {
	u := priv(id, msg, from, text)
	u.Message.Chat.ID, u.Message.Chat.Type = chat, "supergroup"
	return u
}

func TestAGroupTextGoesToTheLayerWithItsConversationAndASecretIsNeverPassedOn(t *testing.T) {
	s, h := newServer(t), &handler{}
	reply := grp(3, 3, 900, -500, "and this")
	reply.Message.ReplyTo = &struct {
		ID int64 `json:"message_id"`
	}{ID: 1}
	s.queue(grp(1, 1, 900, -500, "@bee hi"), grp(2, 2, 901, -500, "/register@thebot s3cret"), reply)
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 3 }, "all three")
	got := h.inbound()
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "900", Native: "-500:1", Text: "@bee hi", Raw: "@bee hi", Group: true, Conversation: "-500"}, got[0],
		"the sender is the person, the conversation the group")
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "901", Native: "-500:2", Registration: true, Group: true, Conversation: "-500"}, got[1],
		"a registration from a group carries no secret: the layer refuses it, and it is not kept")
	assert.Equal(t, "-500:1", got[2].ReplyNative)
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.sent, "what the layer leaves unanswered is not answered")
}

func TestTheLayersAnswerToAGroupMessageGoesToTheGroup(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: "pao says"}}
	s.queue(grp(1, 1, 900, -500, "/pao help"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(s.sentTo(-500)) == 1 }, "the answer")
	assert.Equal(t, []string{"pao says"}, s.sentTo(-500))
	assert.Empty(t, s.sentTo(900), "not to the sender privately")
}

func TestAStickyAddressIsPerSenderAndPerConversation(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}}
	s.queue(grp(1, 1, 900, -500, "/pao agent bee one"), grp(2, 2, 900, -500, "two"), priv(3, 3, 900, "three"),
		grp(4, 4, 900, -600, "four"), grp(5, 5, 901, -500, "five"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 5 }, "five")
	in := h.inbound()
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, in[1].Target, "kept in the group it was made in")
	assert.Zero(t, in[2].Target, "an address made in a group does not steer the private conversation")
	assert.Zero(t, in[3].Target, "nor another group")
	assert.Zero(t, in[4].Target, "nor another sender in the same group")
}

func TestAGroupsMoveIsHandedToTheLayerWithBothIds(t *testing.T) {
	s, h := newServer(t), &handler{}
	to := grp(1, 1, 900, -500, "")
	to.Message.Chat.Type, to.Message.MigrateTo = "group", -1009
	from := grp(2, 1, 900, -1009, "")
	from.Message.From, from.Message.MigrateFrom = nil, -500
	s.queue(to, from)
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "both reports")
	want := comm.ChannelInbound{Channel: "chat", Sender: "-500", Group: true, Conversation: "-500", MovedTo: "-1009"}
	assert.Equal(t, []comm.ChannelInbound{want, want}, h.inbound(), "the old group's report and the new one's say the same")
}

func TestAJoinIsHandedToTheLayerForEachMemberButTheBotItself(t *testing.T) {
	s, h := newServer(t), &handler{}
	j := grp(1, 1, 900, -500, "")
	j.Message.NewMembers = []member{{ID: 777}, {ID: 123, IsBot: true}, {ID: 888, IsBot: true}}
	s.queue(j, priv(2, 2, 900, "after"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 3 }, "two joins and the private message")
	in := h.inbound()
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "777", Group: true, Conversation: "-500", Join: true}, in[0])
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "888", Group: true, Conversation: "-500", Join: true}, in[1],
		"another bot is a reader too; only this bot, whose id the credential carries, is left out")
	assert.Equal(t, "900", in[2].Sender)
}

func TestOnlyATextFromAPersonIsConsidered(t *testing.T) {
	s, h := newServer(t), &handler{}
	bot := priv(2, 2, 901, "@bee hi")
	bot.Message.From.IsBot = true
	nofrom := priv(3, 3, 902, "@bee hi")
	nofrom.Message.From = nil
	notext := priv(4, 4, 903, "")
	s.queue(bot, nofrom, notext, update{ID: 5}, priv(6, 6, 904, "@bee ok"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")
	assert.Equal(t, "904", h.inbound()[0].Sender, "only the last was a private text from a person")
}

func TestTheOffsetAdvancesPastWhatWasHandled(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(7, 1, 900, "@bee a"), priv(9, 2, 900, "@bee b"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "both")
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.polls) >= 2 }, "second poll")
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, "1", s.polls[0]["offset"], "nothing confirmed yet: update ids start at 1")
	assert.Equal(t, "10", s.polls[1]["offset"], "one past the last update handled")
	assert.Empty(t, s.polls[0]["timeout"], "poll_timeout 0 asks for no wait")
	assert.Equal(t, `["message","callback_query"]`, s.polls[0]["allowed_updates"], "messages, and presses of pao's buttons")
}

func TestRegisterIsTheRegistrationEntryPointAndItsSecretIsNotARoutedMessage(t *testing.T) {
	for _, text := range []string{"/register AAAA-BBBB", "/register@somebot AAAA-BBBB"} {
		s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: comm.ReplyRegistered}}
		s.queue(priv(1, 1, 900, text))
		start(t, s, h, nil)
		waitFor(t, func() bool { return len(h.inbound()) == 1 }, text)
		in := h.inbound()[0]
		assert.True(t, in.Registration, text)
		assert.Equal(t, "AAAA-BBBB", in.Text, text)
		assert.Zero(t, in.Target, text)
		waitFor(t, func() bool { return len(s.sentTo(900)) == 1 }, "reply")
		assert.Equal(t, []string{comm.ReplyRegistered}, s.sentTo(900))
	}
	s, h := newServer(t), &handler{}
	s.queue(priv(1, 1, 900, "/register"), priv(2, 2, 900, "/registered-nurse is not it"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "two")
	assert.True(t, h.inbound()[0].Registration)
	assert.Empty(t, h.inbound()[0].Text)
	assert.False(t, h.inbound()[1].Registration, "only the command itself")
}

func TestAddressingByNameTagAndStickyAndReply(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}}
	reply := priv(5, 5, 900, "yes please")
	reply.Message.ReplyTo = &struct {
		ID int64 `json:"message_id"`
	}{ID: 88}
	s.queue(priv(1, 1, 900, "/pao agent bee one"), priv(2, 2, 900, "two"), priv(3, 3, 900, "/pao tag team three"),
		priv(4, 4, 900, "four"), reply, priv(6, 6, 901, "no one yet"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 6 }, "six")
	in := h.inbound()
	assert.Zero(t, in[0].Target, "the first message has no address yet; the layer reads the command")
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, in[1].Target, "no address: the last one the sender used")
	assert.Equal(t, "two", in[1].Text)
	assert.Equal(t, "/pao tag team three", in[2].Text)
	assert.Equal(t, core.Target{Kind: core.ByTag, Value: "team"}, in[3].Target, "a tag address is sticky too")
	assert.Zero(t, in[4].Target, "a reply leaves the address to the layer: the agent that wrote what it answers")
	assert.Equal(t, "900:88", in[4].ReplyNative)
	assert.Zero(t, in[5].Target, "stickiness is per sender")
}

// A leading @name or #tag is not an address (ADR-0026 point 9): it is text, and
// sets no sticky address.
func TestALeadingAtOrHashIsPlainText(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}}
	s.queue(priv(1, 1, 900, "@bee one"), priv(2, 2, 900, "#team two"), priv(3, 3, 900, "three"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 3 }, "three")
	for i, in := range h.inbound() {
		assert.Zero(t, in.Target, "message %d", i)
	}
	assert.Equal(t, "@bee one", h.inbound()[0].Text)
	assert.Equal(t, "#team two", h.inbound()[1].Text)
}

func TestAMessageThatNamesNoOneGetsTheUsageAndAFailureIsToldToTheOperator(t *testing.T) {
	s, h := newServer(t), &handler{err: core.ErrNoTarget}
	s.queue(priv(1, 1, 900, "hello"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(s.sentTo(900)) == 1 }, "usage")
	assert.Contains(t, s.sentTo(900)[0], "/pao agent <name>")

	s2, h2 := newServer(t), &handler{err: fmt.Errorf("%w: agent %q", core.ErrNoMatch, "ghost")}
	s2.queue(priv(1, 1, 900, "@ghost hi"))
	start(t, s2, h2, nil)
	waitFor(t, func() bool { return len(s2.sentTo(900)) == 1 }, "error")
	assert.Contains(t, s2.sentTo(900)[0], "ghost")
}

func TestAFailureToRecordIsLoggedAndNeverSentToTheSender(t *testing.T) {
	s, h := newServer(t), &handler{err: fmt.Errorf("%w: disk full", comm.ErrRecord)}
	s.queue(priv(1, 1, 900, "@bee hi"), priv(2, 2, 901, "@bee hi"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "both handled")
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, s.sentTo(900))
	assert.Empty(t, s.sentTo(901))
}

func TestASendersMessagesAreHandledOneAtATimeInOrder(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}, slowFirst: 150 * time.Millisecond}
	s.queue(priv(1, 1, 900, "/pao bee one"), priv(2, 2, 900, "two"), priv(3, 3, 900, "three"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 3 }, "three")
	in := h.inbound()
	assert.Equal(t, []string{"/pao bee one", "two", "three"}, []string{in[0].Text, in[1].Text, in[2].Text}, "a slow message is not overtaken")
	assert.Equal(t, "bee", in[1].Target.Value, "so the next message finds the address the slow one set")
}

func TestAPollFailureIsNotLoggedOnceRunIsStopping(t *testing.T) {
	var buf strings.Builder
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	b, err := newBot(spec(newServer(t), nil), &handler{})
	require.NoError(t, err)

	b.pollFailed(errors.New("x"))
	ctx, cancel := context.WithCancel(context.Background())
	b.runCtx = ctx
	b.pollFailed(errors.New("x"))
	cancel()
	b.pollFailed(errors.New("x"))
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, strings.Count(buf.String(), "channel poll failed"), "logged before Run and while running, not once stopping")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestAStickyAddressIsKeptOnlyWhenTheMessageWasRouted(t *testing.T) {
	s, h := newServer(t), &handler{} // nothing delivered: the sender is a stranger
	s.queue(priv(1, 1, 900, "/pao bee hi"), priv(2, 2, 900, "again"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "two")
	assert.Zero(t, h.inbound()[1].Target, "a stranger's address is not remembered")
}

func TestAFailedPollIsRetriedAndThenWorksAgain(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.failNow = 3
	s.queue(priv(1, 1, 900, "@bee hi"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "recovered")
	s.mu.Lock()
	assert.GreaterOrEqual(t, len(s.polls), 4)
	s.mu.Unlock()
}

func TestPaoOutputGoesToEachRecipientAsALineAndIsNamedByTheFirstPiece(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900", "901"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	natives, err := b.SendNative(context.Background(), core.Message{Sender: "bee", Text: "done"})
	require.NoError(t, err)
	assert.Equal(t, []string{"[bee]: done"}, s.sentTo(900))
	assert.Equal(t, []string{"[bee]: done"}, s.sentTo(901))
	assert.Equal(t, []string{"900:101", "901:102"}, natives)
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "bee", Text: "again"}))
}

func TestTheChannelsDefaultAgentSpeaksUnprefixedAndAnyOtherIsNamed(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}, defaults: map[string]string{"chat": "bee", "other": "wasp"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "bee", Text: "from the default"}))
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "wasp", Text: "from another"}))
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "beetle", Text: "a longer name"}))
	assert.Equal(t, []string{"from the default", "[wasp]: from another", "[beetle]: a longer name"}, s.sentTo(900),
		"only this channel's default drops the prefix; another channel's default is named here")
}

func TestALabelReplacesTheNameEvenForTheDefaultAgent(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}, defaults: map[string]string{"chat": "bee"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "bee", Text: "one", Label: "TA:"}))
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "wasp", Text: "two", Label: "W"}))
	assert.Equal(t, []string{"TA: one", "W two"}, s.sentTo(900))
}

func TestALongMessageIsSplitAtTheConfiguredLimitAndNamedByItsFirstPiece(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}}
	b, err := newBot(spec(s, map[string]any{"message_limit": 100}), h)
	require.NoError(t, err)
	text := strings.Repeat("word ", 50)
	natives, err := b.SendNative(context.Background(), core.Message{Sender: "bee", Text: text})
	require.NoError(t, err)
	parts := s.sentTo(900)
	require.Greater(t, len(parts), 2)
	for _, p := range parts {
		assert.LessOrEqual(t, len([]rune(p)), 100)
	}
	assert.Equal(t, strings.TrimSpace("[bee]: "+text), strings.TrimSpace(strings.Join(parts, " ")), "nothing lost, only cut at spaces")
	assert.Equal(t, []string{"900:101"}, natives, "the first piece names the message")
}

func TestOneRecipientBlockingTheBotDoesNotStopTheRest(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900", "901"}}
	s.failSend[900] = true
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	natives, err := b.SendNative(context.Background(), core.Message{Sender: "bee", Text: "x"})
	require.Error(t, err)
	assert.Equal(t, []string{"[bee]: x"}, s.sentTo(901))
	assert.Equal(t, []string{"901:101"}, natives, "what went out is still named")

	h.recipients = []string{"not-a-chat"}
	_, err = b.SendNative(context.Background(), core.Message{Sender: "bee", Text: "x"})
	require.Error(t, err)
}

func TestTheTokenIsNeverInAnError(t *testing.T) {
	s := newServer(t)
	b, err := newBot(spec(s, nil), &handler{})
	require.NoError(t, err)
	s.ts.Close()
	_, err = b.send(context.Background(), addr{chat: 900}, "x", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok, "a transport error names the URL, the URL holds the token")
	assert.NotContains(t, err.Error(), "http://", "and the URL itself is not in the log either")
	assert.Contains(t, err.Error(), "transport")

	s2 := newServer(t)
	sp := spec(s2, nil)
	sp.Credential = "WRONG"
	b2, err := newBot(sp, &handler{})
	require.NoError(t, err)
	_, err = b2.send(context.Background(), addr{chat: 900}, "x", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "WRONG")
	assert.Equal(t, "the service's answer could not be used", err.Error())
}

func TestAnErrorIsInTheAdaptersWordsNotTheLibrarys(t *testing.T) {
	s := newServer(t)
	s.mode = "unknown"
	b, err := newBot(spec(s, nil), &handler{})
	require.NoError(t, err)
	_, err = b.send(context.Background(), addr{chat: 900}, "x", nil)
	require.Error(t, err)
	assert.Equal(t, "the service's answer could not be used", err.Error(), "an answer the adapter has no word for is not quoted")

	s.failSend[901] = true
	_, err = b.send(context.Background(), addr{chat: 901}, "x", nil)
	assert.EqualError(t, err, "forbidden: blocked", "a known kind keeps what the service said")
}

func TestAnAnswerPastTheCapIsRefused(t *testing.T) {
	s := newServer(t)
	s.huge = true
	b, err := newBot(spec(s, nil), &handler{})
	require.NoError(t, err)
	_, err = b.send(context.Background(), addr{chat: 900}, "x", nil)
	assert.EqualError(t, err, "the service's answer was too large")
}

func TestSplitKeepsEverythingAndPrefersLinesThenWords(t *testing.T) {
	assert.Equal(t, []string{""}, split("", 10))
	assert.Equal(t, []string{"short"}, split("short", 10))
	assert.Equal(t, []string{"aaaa", "bbbb"}, split("aaaa\nbbbb", 6), "a line break is the cut")
	assert.Equal(t, []string{"aaaa", "bbbb"}, split("aaaa bbbb", 6), "else a space")
	assert.Equal(t, []string{"aaaaaa", "bbbb"}, split("aaaaaabbbb", 6), "else the limit")
	assert.Equal(t, []string{"ééé", "éé"}, split("ééééé", 3), "by characters, not bytes")
}

func TestARunThatIsCancelledReturnsNilEvenWhileFailing(t *testing.T) {
	s := newServer(t)
	s.failNow = 1 << 30
	_, cancel := start(t, s, &handler{}, nil)
	time.Sleep(30 * time.Millisecond)
	cancel()
}

func TestNothingIsSentWhenThereIsNothingToSay(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}}
	s.queue(priv(1, 1, 900, "@bee hi"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.sent, "a routed message is not answered: the agent's output is")
}

func TestTheTokenIsScrubbedFromWhatTheServiceSays(t *testing.T) {
	s := newServer(t)
	s.mode, s.describe = "okfalse", "your token "+tok+" is bad"
	b, err := newBot(spec(s, nil), &handler{})
	require.NoError(t, err)
	_, err = b.send(context.Background(), addr{chat: 900}, "x", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)
	assert.Equal(t, "bad request: your token *** is bad", err.Error())
}

func TestTheRawPayloadKeptForTheRecordIsBounded(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(1, 1, 900, "@bee "+strings.Repeat("x", 10000)))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")
	assert.Len(t, []rune(h.inbound()[0].Raw), 4096)
	assert.Greater(t, len(h.inbound()[0].Text), 9000, "the message itself is not cut")
}

// buttons reads the buttons a send carried: label and payload, row by row.
func buttons(t *testing.T, sent map[string]string) [][2]string {
	t.Helper()
	raw, ok := sent["reply_markup"]
	if !ok {
		return nil
	}
	var mk struct {
		Rows [][]struct {
			Text string `json:"text"`
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &mk), raw)
	var out [][2]string
	for _, row := range mk.Rows {
		for _, b := range row {
			out = append(out, [2]string{b.Text, b.Data})
		}
	}
	return out
}

var gateActions = []core.Action{
	{Label: "ALLOW", Command: "/pao allow q1"},
	{Label: "ALWAYS", Command: "/pao always q1"},
	{Label: "DENY", Command: "/pao deny q1"},
}

func TestAMessagesActionsAreButtonsUnderItsLastPieceCarryingTheirCommands(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900", "901"}}
	b, err := newBot(spec(s, map[string]any{"message_limit": 100}), h)
	require.NoError(t, err)
	_, err = b.SendNative(context.Background(), core.Message{ID: "m7", Sender: "pao", Text: strings.Repeat("word ", 30), Actions: gateActions})
	require.NoError(t, err)
	sent := s.snapshot(&s.sent)
	require.Len(t, sent, 4, "two pieces to each of two recipients")
	want := [][2]string{{"ALLOW", "/pao allow q1"}, {"ALWAYS", "/pao always q1"}, {"DENY", "/pao deny q1"}}
	for i, m := range sent {
		if i%2 == 0 {
			assert.Nil(t, buttons(t, m), "no buttons on a first piece")
		} else {
			assert.Equal(t, want, buttons(t, m), "the buttons are under the last piece")
		}
	}

	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m8", Sender: "pao", Text: "plain"}))
	assert.Nil(t, buttons(t, s.snapshot(&s.sent)[4]), "a message with no actions has no buttons")
}

func TestAnActionThatDoesNotFitAButtonGetsNone(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	fits := "/pao allow " + strings.Repeat("x", maxPayload-len("/pao allow "))
	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m1", Sender: "pao", Text: "q", Actions: []core.Action{
		{Label: "FITS", Command: fits},
		{Label: "LONG", Command: fits + "x"},
		{Label: "", Command: "/pao deny q1"},
		{Label: "EMPTY"},
	}}))
	assert.Equal(t, [][2]string{{"FITS", fits}}, buttons(t, s.snapshot(&s.sent)[0]))

	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m2", Sender: "pao", Text: "q", Actions: []core.Action{{Label: "LONG", Command: fits + "x"}}}))
	assert.Nil(t, buttons(t, s.snapshot(&s.sent)[1]), "no button at all when none fits")
	require.NoError(t, b.CloseActions(context.Background(), "m2", "denied"))
	assert.Empty(t, s.snapshot(&s.edits), "nothing was shown with buttons, so nothing is closed")
}

func TestClosingRemovesTheButtonsFromEveryCopyAndShowsTheOutcomeOnce(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900", "901"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m7", Sender: "pao", Text: "may I?", Actions: gateActions}))
	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m8", Sender: "pao", Text: "and this?", Actions: gateActions}))

	require.NoError(t, b.CloseActions(context.Background(), "m7", "allowed"))
	edits := s.snapshot(&s.edits)
	require.Len(t, edits, 2, "one edit per copy of m7, none of m8")
	for i, e := range edits {
		assert.Equal(t, []string{"900", "901"}[i], e["chat_id"])
		assert.Equal(t, []string{"101", "102"}[i], e["message_id"])
		assert.Equal(t, "[pao]: may I?\n\n— allowed", e["text"])
		assert.JSONEq(t, `{"inline_keyboard":[]}`, e["reply_markup"], "the buttons are removed")
	}
	require.NoError(t, b.CloseActions(context.Background(), "m7", "denied"))
	assert.Len(t, s.snapshot(&s.edits), 2, "a closed message is not closed again")
	require.NoError(t, b.CloseActions(context.Background(), "m8", "expired"))
	assert.Len(t, s.snapshot(&s.edits), 4)
}

func TestAFailedCloseIsReportedInTheAdaptersWords(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	require.NoError(t, b.Send(context.Background(), core.Message{ID: "m7", Sender: "pao", Text: "may I?", Actions: gateActions}))
	s.mu.Lock()
	s.failEdit = true
	s.mu.Unlock()
	err = b.CloseActions(context.Background(), "m7", "allowed")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)
}

func TestAClosedTextStaysWithinTheLimit(t *testing.T) {
	assert.Equal(t, "short\n\n— denied", closed("short", "denied", 100))
	got := closed(strings.Repeat("é", 100), "denied", 100)
	assert.Len(t, []rune(got), 100)
	assert.True(t, strings.HasSuffix(got, "…\n\n— denied"), got)
}

func TestOnlyTheNewestMessagesButtonsAreKeptForClosing(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}}
	b, err := newBot(spec(s, nil), h)
	require.NoError(t, err)
	for i := 0; i <= maxShown; i++ {
		require.NoError(t, b.Send(context.Background(), core.Message{ID: "m" + strconv.Itoa(i), Sender: "pao", Text: "q", Actions: gateActions}))
	}
	require.NoError(t, b.CloseActions(context.Background(), "m0", "expired"))
	assert.Empty(t, s.snapshot(&s.edits), "the oldest was forgotten")
	require.NoError(t, b.CloseActions(context.Background(), "m1", "expired"))
	assert.Len(t, s.snapshot(&s.edits), 1, "the next one is still kept")
}

func TestAPressReachesTheLayerAsTheCommandFromThePresserAndIsAnsweredToThemAlone(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: "q1: allow"}}
	start(t, s, h, nil)
	s.queue(press(1, "cb1", 42, 42, "private", 101, "/pao allow q1"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
	in := h.inbound()
	require.Len(t, in, 1)
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "42", Text: "/pao allow q1", Raw: "/pao allow q1", Press: true, Pressed: native(42, 101)}, in[0],
		"the press is the command, from the service's sender of the press, with no target and no sticky address, naming the message pressed")
	a := s.snapshot(&s.answers)[0]
	assert.Equal(t, "cb1", a["callback_query_id"])
	assert.Equal(t, "q1: allow", a["text"])
	assert.Empty(t, s.snapshot(&s.sent), "the answer is the press's own, not a message to the chat")
}

func TestAReplyThatDoesNotFitAPressAnswerIsSentToThePresserPrivately(t *testing.T) {
	for name, c := range map[string]struct {
		reply string
		chat  int64
		kind  string
	}{
		"long":                 {strings.Repeat("y", 500), 42, "private"},
		"several lines":        {"agents you may reach:\nbee: stopped", 42, "private"},
		"pressed in a group":   {"agents you may reach:\nbee: stopped", -500, "group"},
		"one line at the edge": {strings.Repeat("y", maxPressAnswer+1), 42, "private"},
	} {
		s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: c.reply}}
		start(t, s, h, nil)
		s.queue(press(1, "cb1", 42, c.chat, c.kind, 101, "/pao agents"))
		waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
		sent := s.snapshot(&s.sent)
		require.Len(t, sent, 1, name)
		assert.Equal(t, "42", sent[0]["chat_id"], "%s: the presser's private chat, never the chat pressed in", name)
		assert.Equal(t, c.reply, sent[0]["text"], "%s: in full", name)
		assert.Equal(t, pressAnsweredPrivately, s.snapshot(&s.answers)[0]["text"], name)
	}
}

func TestAReplyThatFitsStaysThePressAnswer(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: strings.Repeat("y", maxPressAnswer)}}
	start(t, s, h, nil)
	s.queue(press(1, "cb1", 42, 42, "private", 101, "/pao allow q1"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
	assert.Len(t, s.snapshot(&s.answers)[0]["text"], maxPressAnswer)
	assert.Empty(t, s.snapshot(&s.sent))
}

func TestAPressAnswerThatCannotReachThePresserIsCutToWhatAPressAnswerHolds(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: strings.Repeat("y", 500)}}
	s.failSend[42] = true
	start(t, s, h, nil)
	s.queue(press(1, "cb1", 42, 42, "private", 101, "/pao help"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
	assert.Len(t, s.snapshot(&s.answers)[0]["text"], maxPressAnswer, "the notice, cut short")
}

func TestAPressInAGroupOrOnNoMessageIsMarkedAGroup(t *testing.T) {
	s, h := newServer(t), &handler{}
	start(t, s, h, nil)
	old := press(3, "cb3", 42, -500, "group", 102, "/pao allow q1")
	old.Callback.Message.Date = 0
	oldPrivate := press(4, "cb4", 42, 42, "private", 103, "/pao allow q1")
	oldPrivate.Callback.Message.Date = 0
	s.queue(press(1, "cb1", 42, -500, "group", 101, "/pao allow q1"), press(2, "cb2", 42, 0, "", 0, "/pao allow q1"), old, oldPrivate)
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 4 }, "every press is answered")
	in := h.inbound()
	require.Len(t, in, 4)
	assert.True(t, in[0].Group && in[0].Press, "a press under a group's message")
	assert.Equal(t, "-500", in[0].Conversation)
	assert.Equal(t, native(-500, 101), in[0].Pressed)
	assert.True(t, in[1].Group && in[1].Press, "a press on no message")
	assert.Empty(t, in[1].Pressed)
	assert.Empty(t, in[2].Pressed, "a message the bot can no longer reach is not named")
	assert.Empty(t, in[1].Conversation, "one that names no conversation, which no group admits")
	assert.True(t, in[2].Group && in[2].Press, "a press under a group's message the bot can no longer reach")
	assert.Equal(t, "-500", in[2].Conversation)
	assert.False(t, in[3].Group, "a private chat is read from a message the bot can no longer reach, too")
	assert.Empty(t, in[3].Conversation)
	assert.Empty(t, s.snapshot(&s.answers)[0]["text"])
}

func TestAPressByABotOrWithNoPayloadIsAnsweredAndNotTakenIn(t *testing.T) {
	s, h := newServer(t), &handler{}
	start(t, s, h, nil)
	botPress := press(1, "cb1", 42, 42, "private", 101, "/pao allow q1")
	botPress.Callback.From.IsBot = true
	s.queue(botPress, press(2, "cb2", 42, 42, "private", 101, ""))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 2 }, "both presses are answered, so the client stops waiting")
	assert.Empty(t, h.inbound())
}

func TestAFailureToHandleAPressIsItsAnswer(t *testing.T) {
	s, h := newServer(t), &handler{err: errors.New("no agent")}
	start(t, s, h, nil)
	s.queue(press(1, "cb1", 42, 42, "private", 101, "/pao allow q1"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
	assert.Equal(t, "no agent", s.snapshot(&s.answers)[0]["text"])

	h.mu.Lock()
	h.err = fmt.Errorf("%w: disk", comm.ErrRecord)
	h.mu.Unlock()
	s.queue(press(2, "cb2", 42, 42, "private", 101, "/pao allow q1"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 2 }, "the press is answered")
	assert.Empty(t, s.snapshot(&s.answers)[1]["text"], "a failure to record is logged, never told")
}

func TestTheBotsOwnIdIsReadFromItsCredential(t *testing.T) {
	assert.Equal(t, int64(123), selfID("123:TOK"))
	for _, c := range []string{"TOK", "abc:TOK", ":TOK", ""} {
		assert.Zero(t, selfID(c), c)
	}
}
