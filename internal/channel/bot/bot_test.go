package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

const tok = "TOK-123"

// server is a fake of the service's API: it hands out queued batches of updates,
// records what is sent, and can be made to fail.
type server struct {
	t  *testing.T
	ts *httptest.Server

	mu       sync.Mutex
	batches  [][]update
	polls    []map[string]any
	sent     []map[string]any
	nextID   int64
	failNow  int              // polls to fail before serving
	failSend map[float64]bool // chat ids whose sends fail
	// mode shapes the answer to a poll: "" is fine, "okfalse" is a 200 with ok false, "status" a 500 with ok true.
	mode string
	// describe, when set, is put in a failure's description.
	describe string
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
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	switch strings.TrimPrefix(r.URL.Path, "/bot"+tok+"/") {
	case "getUpdates":
		s.polls = append(s.polls, body)
		if s.failNow > 0 {
			s.failNow--
			w.WriteHeader(500)
			_, _ = fmt.Fprintf(w, `{"ok":false,"description":"boom %s"}`, s.describe)
			return
		}
		switch s.mode {
		case "okfalse":
			_, _ = fmt.Fprintf(w, `{"ok":false,"description":%q}`, s.describe)
			return
		case "status":
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
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
		if s.failSend[body["chat_id"].(float64)] {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"ok":false,"description":"blocked"}`)
			return
		}
		s.sent = append(s.sent, body)
		s.nextID++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, s.nextID)
	default:
		http.Error(w, "nope", 404)
	}
}

func (s *server) queue(us ...update) { s.mu.Lock(); s.batches = append(s.batches, us); s.mu.Unlock() }

func (s *server) sentTo(chat float64) []string {
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
}

func (h *handler) ReceiveFromChannel(_ context.Context, in comm.ChannelInbound) (comm.ChannelResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, in)
	return h.res, h.err
}
func (h *handler) Recipients(string) []string             { return h.recipients }
func (h *handler) NativeOf(string, string) (string, bool) { return "", false }
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
	b, err := newBot(spec(s, opts), h, nil)
	require.NoError(t, err)
	b.startBackoff, b.maxBackoff = time.Millisecond, 5*time.Millisecond
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
	good := map[string]any{"endpoint": "https://x.example/{token}/{method}"}
	for name, c := range map[string]struct {
		opts map[string]any
		want string
	}{
		"none":            {nil, "endpoint"},
		"no token":        {map[string]any{"endpoint": "https://x/{method}"}, "endpoint"},
		"no method":       {map[string]any{"endpoint": "https://x/{token}"}, "endpoint"},
		"not a url":       {map[string]any{"endpoint": "ftp://x/{token}/{method}"}, "http or https"},
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
	}
	a, err := New(adapter.Spec{Name: "chat", Credential: tok, Options: map[string]any{"endpoint": good["endpoint"], "poll_timeout": 5.0, "message_limit": uint64(500)}}, &handler{})
	require.NoError(t, err)
	assert.Equal(t, "chat", a.Name())
	assert.Equal(t, 500, a.(*Bot).limit)
	assert.Equal(t, 5*time.Second, a.(*Bot).poll)
}

func TestAPrivateMessageReachesTheLayerWithTheServicesSenderIdAndNativeIds(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(1, 55, 900, "@bee hello there"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")

	in := h.inbound()[0]
	assert.Equal(t, comm.ChannelInbound{Channel: "chat", Sender: "900", Native: "900:55", Text: "hello there", Raw: "@bee hello there",
		Target: core.Target{Kind: core.ByName, Value: "bee"}}, in)
}

func TestOnlyAPrivateTextFromAPersonIsConsidered(t *testing.T) {
	s, h := newServer(t), &handler{}
	grp := priv(1, 1, 900, "@bee hi")
	grp.Message.Chat.Type = "group"
	bot := priv(2, 2, 901, "@bee hi")
	bot.Message.From.IsBot = true
	nofrom := priv(3, 3, 902, "@bee hi")
	nofrom.Message.From = nil
	notext := priv(4, 4, 903, "")
	s.queue(grp, bot, nofrom, notext, update{ID: 5}, priv(6, 6, 904, "@bee ok"))
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
	assert.EqualValues(t, 0, s.polls[0]["offset"])
	assert.EqualValues(t, 10, s.polls[1]["offset"], "one past the highest update handled")
	assert.EqualValues(t, 0, s.polls[0]["timeout"])
	assert.Equal(t, []any{"message"}, s.polls[0]["allowed_updates"])
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
	reply := priv(4, 4, 900, "yes please")
	reply.Message.ReplyTo = &struct {
		ID int64 `json:"message_id"`
	}{ID: 88}
	s.queue(priv(1, 1, 900, "@bee one"), priv(2, 2, 900, "two"), priv(3, 3, 900, "#team three"), reply, priv(5, 5, 901, "no one yet"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 5 }, "five")
	in := h.inbound()
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, in[0].Target)
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, in[1].Target, "no address: the last one the sender used")
	assert.Equal(t, "two", in[1].Text)
	assert.Equal(t, core.Target{Kind: core.ByTag, Value: "team"}, in[2].Target)
	assert.Zero(t, in[3].Target, "a reply leaves the address to the layer: the agent that wrote what it answers")
	assert.Equal(t, "900:88", in[3].ReplyNative)
	assert.Zero(t, in[4].Target, "stickiness is per sender")
}

func TestAMessageThatNamesNoOneGetsTheUsageAndAFailureIsToldToTheOperator(t *testing.T) {
	s, h := newServer(t), &handler{err: core.ErrNoTarget}
	s.queue(priv(1, 1, 900, "hello"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(s.sentTo(900)) == 1 }, "usage")
	assert.Contains(t, s.sentTo(900)[0], "@name")

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

func TestAStickyAddressIsKeptOnlyWhenTheMessageWasRouted(t *testing.T) {
	s, h := newServer(t), &handler{} // nothing delivered: the sender is a stranger
	s.queue(priv(1, 1, 900, "@bee hi"), priv(2, 2, 900, "again"))
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
	b, err := newBot(spec(s, nil), h, nil)
	require.NoError(t, err)
	natives, err := b.SendNative(context.Background(), core.Message{Sender: "bee", Text: "done"})
	require.NoError(t, err)
	assert.Equal(t, []string{"[bee]: done"}, s.sentTo(900))
	assert.Equal(t, []string{"[bee]: done"}, s.sentTo(901))
	assert.Equal(t, []string{"900:101", "901:102"}, natives)
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "bee", Text: "again"}))
}

func TestALongMessageIsSplitAtTheConfiguredLimitAndNamedByItsFirstPiece(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"900"}}
	b, err := newBot(spec(s, map[string]any{"message_limit": 100}), h, nil)
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
	b, err := newBot(spec(s, nil), h, nil)
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
	b, err := newBot(spec(s, nil), &handler{}, nil)
	require.NoError(t, err)
	s.ts.Close()
	err = b.call(context.Background(), "getUpdates", map[string]any{}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok, "a transport error names the URL, the URL holds the token")
	assert.NotContains(t, err.Error(), "http://", "and the URL itself is not in the log either")

	s2 := newServer(t)
	b2, err := newBot(spec(s2, nil), &handler{}, nil)
	require.NoError(t, err)
	b2.token = "WRONG"
	err = b2.call(context.Background(), "getUpdates", map[string]any{}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "WRONG")
	assert.Contains(t, err.Error(), "404")
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

var _ = errors.New

func TestAnUpdateOutOfOrderDoesNotMoveTheOffsetBack(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(9, 1, 900, "@bee a"), priv(7, 2, 900, "@bee b"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 2 }, "both")
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.polls) >= 2 }, "second poll")
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.EqualValues(t, 10, s.polls[1]["offset"])
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
	b, err := newBot(spec(s, nil), &handler{}, nil)
	require.NoError(t, err)
	err = b.call(context.Background(), "getUpdates", map[string]any{}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)
	assert.Contains(t, err.Error(), "***")
}

func TestAnAnswerIsGoodOnlyIfTheStatusAndTheOkFlagAreBoth(t *testing.T) {
	for _, mode := range []string{"okfalse", "status"} {
		s := newServer(t)
		s.mode = mode
		b, err := newBot(spec(s, nil), &handler{}, nil)
		require.NoError(t, err)
		var out []update
		assert.Error(t, b.call(context.Background(), "getUpdates", map[string]any{}, &out), mode)
	}
}

func TestTheRetryPauseGrowsToACapAndStartsAgainAfterASuccess(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.failNow = 5
	b, err := newBot(spec(s, nil), h, nil)
	require.NoError(t, err)
	b.startBackoff, b.maxBackoff = time.Millisecond, 4*time.Millisecond
	var mu sync.Mutex
	var pauses []time.Duration
	b.wait = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		pauses = append(pauses, d)
		mu.Unlock()
		return ctx.Err() == nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = b.Run(ctx); close(done) }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.polls) >= 7 }, "recovered and polling")
	s.mu.Lock()
	s.failNow = 2
	s.mu.Unlock()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(pauses) >= 7 }, "failed again")
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	ms := time.Millisecond
	assert.Equal(t, []time.Duration{ms, 2 * ms, 4 * ms, 4 * ms, 4 * ms, ms, 2 * ms}, pauses[:7], "doubles, stops at the cap, and a success starts it over")
}

func TestAnAddressNeedsAMarkerAndAName(t *testing.T) {
	for _, text := range []string{"@", "#", "x", "@ bee", "plain words"} {
		_, rest, explicit := parseAddress(text)
		assert.False(t, explicit, text)
		assert.Equal(t, text, rest, text)
	}
	tg, rest, explicit := parseAddress("  @bee   hello world ")
	assert.True(t, explicit)
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, tg)
	assert.Equal(t, "hello world", rest)
	tg, rest, explicit = parseAddress("@bee")
	assert.True(t, explicit)
	assert.Equal(t, "bee", tg.Value)
	assert.Empty(t, rest)
}

func TestTheRawPayloadKeptForTheRecordIsBounded(t *testing.T) {
	s, h := newServer(t), &handler{}
	s.queue(priv(1, 1, 900, "@bee "+strings.Repeat("x", 10000)))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 1 }, "inbound")
	assert.Len(t, []rune(h.inbound()[0].Raw), 4096)
	assert.Greater(t, len(h.inbound()[0].Text), 9000, "the message itself is not cut")
}
