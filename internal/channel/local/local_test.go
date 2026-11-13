package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

const token = "s3cret-token"

type fakeLayer struct {
	mu        sync.Mutex
	inbound   []comm.Inbound
	reactions [][3]string
	err       error
}

func (f *fakeLayer) Receive(_ context.Context, in comm.Inbound) (comm.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return comm.Delivery{}, f.err
	}
	f.inbound = append(f.inbound, in)
	return comm.Delivery{
		Message: core.Message{ID: "m1"},
		Results: []route.Result{
			{Agent: "a", Launched: true},
			{Agent: "b", Err: errors.New("paste failed")},
		},
	}, nil
}

func (f *fakeLayer) React(id, by, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.reactions = append(f.reactions, [3]string{id, by, emoji})
	return nil
}

func newServer(t *testing.T, l Layer, opts ...Option) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(l, token, opts...)
	require.NoError(t, err)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, ts *httptest.Server, method, path, body, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bearer() string { return "Bearer " + token }

func TestNewRefusesAnEmptyToken(t *testing.T) {
	_, err := New(&fakeLayer{}, "")
	assert.Error(t, err)
}

func TestEveryRouteRequiresTheToken(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/messages"}, {"POST", "/v1/reactions"}, {"GET", "/v1/stream"},
	} {
		for _, auth := range []string{"", "Bearer wrong", "Bearer ", token, "Basic " + token} {
			resp := do(t, ts, tc.method, tc.path, `{}`, auth)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s auth=%q", tc.method, tc.path, auth)
		}
	}
}

func TestPostMessage(t *testing.T) {
	fl := &fakeLayer{}
	_, ts := newServer(t, fl)

	resp := do(t, ts, "POST", "/v1/messages",
		`{"sender":"op","text":"hello","target":{"kind":"tag","value":"team"},"reply_quote":"earlier"}`, bearer())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got deliveryJSON
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "m1", got.ID)
	assert.Equal(t, []resultJSON{{Agent: "a", Launched: true}, {Agent: "b", Error: "paste failed"}}, got.Results)

	require.Len(t, fl.inbound, 1)
	in := fl.inbound[0]
	assert.Equal(t, Source, in.Source)
	assert.Equal(t, "op", in.Sender)
	assert.Equal(t, "hello", in.Text)
	assert.Equal(t, core.Target{Kind: core.ByTag, Value: "team"}, in.Target)
	assert.Equal(t, "earlier", in.ReplyQuote)
}

func TestPostMessageDefaultsTheSender(t *testing.T) {
	fl := &fakeLayer{}
	_, ts := newServer(t, fl)

	resp := do(t, ts, "POST", "/v1/messages", `{"text":"hi","target":{"kind":"name","value":"a"}}`, bearer())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "human", fl.inbound[0].Sender)
	assert.Equal(t, core.ByName, fl.inbound[0].Target.Kind)
}

func TestPostMessageRejectsBadInput(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	for name, body := range map[string]string{
		"not json":      `{`,
		"unknown field": `{"text":"x","target":{"kind":"name","value":"a"},"nope":1}`,
		"empty text":    `{"text":"  ","target":{"kind":"name","value":"a"}}`,
		"bad kind":      `{"text":"x","target":{"kind":"everyone","value":"a"}}`,
		"missing kind":  `{"text":"x","target":{"value":"a"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := do(t, ts, "POST", "/v1/messages", body, bearer())
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

func TestPostMessageRejectsAnOversizedBody(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	big := `{"text":"` + strings.Repeat("x", maxBody) + `","target":{"kind":"name","value":"a"}}`
	resp := do(t, ts, "POST", "/v1/messages", big, bearer())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestErrorsMapToStatuses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{core.ErrNoTarget, http.StatusBadRequest},
		{core.ErrNoMatch, http.StatusNotFound},
		{msgstore.ErrNotFound, http.StatusNotFound},
		{msgstore.ErrUnresolved, http.StatusUnprocessableEntity},
		{msgstore.ErrAmbiguous, http.StatusUnprocessableEntity},
	} {
		_, ts := newServer(t, &fakeLayer{err: tc.err})
		resp := do(t, ts, "POST", "/v1/messages", `{"text":"x","target":{"kind":"name","value":"a"}}`, bearer())
		assert.Equal(t, tc.want, resp.StatusCode, tc.err.Error())
	}
}

func TestUnexpectedErrorsDoNotLeakInternals(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{err: errors.New("bolt: /var/secret/path exploded")})
	resp := do(t, ts, "POST", "/v1/messages", `{"text":"x","target":{"kind":"name","value":"a"}}`, bearer())
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "internal error", body["error"])
}

func TestPostReaction(t *testing.T) {
	fl := &fakeLayer{}
	_, ts := newServer(t, fl)

	resp := do(t, ts, "POST", "/v1/reactions", `{"id":"m7","emoji":"👍"}`, bearer())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, [][3]string{{"m7", "human", "👍"}}, fl.reactions)

	resp = do(t, ts, "POST", "/v1/reactions", `{"id":"m7"}`, bearer())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	fl.err = msgstore.ErrNotFound
	resp = do(t, ts, "POST", "/v1/reactions", `{"id":"gone","emoji":"👍"}`, bearer())
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// event reads server-sent events from a stream.
type event struct{ id, name, data string }

func openStream(t *testing.T, ts *httptest.Server) (<-chan event, func()) {
	t.Helper()
	req, err := http.NewRequest("GET", ts.URL+"/v1/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", bearer())
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	out := make(chan event, 16)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		var cur event
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.name != "" {
					out <- cur
				}
				cur = event{}
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out, func() { cancel(); _ = resp.Body.Close() }
}

func next(t *testing.T, ch <-chan event) event {
	t.Helper()
	select {
	case e, ok := <-ch:
		require.True(t, ok, "stream closed")
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
		return event{}
	}
}

func waitSubscribers(t *testing.T, s *Server, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.subs) == n
	}, 3*time.Second, 5*time.Millisecond)
}

func TestSendReachesEveryStream(t *testing.T) {
	s, ts := newServer(t, &fakeLayer{})
	one, stop1 := openStream(t, ts)
	defer stop1()
	two, stop2 := openStream(t, ts)
	defer stop2()
	waitSubscribers(t, s, 2)

	require.NoError(t, s.Send(context.Background(), core.Message{
		ID: "m5", Sender: "a", Kind: core.MessageContent, Text: "line one\nline two", Reply: "m4",
	}))

	for _, ch := range []<-chan event{one, two} {
		e := next(t, ch)
		assert.Equal(t, "m5", e.id)
		assert.Equal(t, "message", e.name)
		var m MessageJSON
		require.NoError(t, json.Unmarshal([]byte(e.data), &m))
		assert.Equal(t, "a", m.Sender)
		assert.Equal(t, "line one\nline two", m.Text, "newlines survive inside the JSON data line")
		assert.Equal(t, "m4", m.Reply)
	}
}

func TestStreamIsRemovedWhenTheClientLeaves(t *testing.T) {
	s, ts := newServer(t, &fakeLayer{})
	_, stop := openStream(t, ts)
	waitSubscribers(t, s, 1)
	stop()
	waitSubscribers(t, s, 0)
}

func TestSlowStreamIsDroppedWithoutBlockingSend(t *testing.T) {
	s, _ := newServer(t, &fakeLayer{})
	slow := make(chan core.Message, subscriberBuf)
	s.mu.Lock()
	s.subs[slow] = struct{}{}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range subscriberBuf + 5 {
			_ = s.Send(context.Background(), core.Message{ID: "m"})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a slow stream")
	}

	s.mu.Lock()
	_, still := s.subs[slow]
	s.mu.Unlock()
	assert.False(t, still, "the slow stream is dropped")
}

func TestHeartbeatKeepsAnIdleStreamAlive(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithHeartbeat(10*time.Millisecond))
	req, err := http.NewRequest("GET", ts.URL+"/v1/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", bearer())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	seen := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == ": keepalive" {
				seen <- true
				return
			}
		}
		seen <- false
	}()
	select {
	case ok := <-seen:
		assert.True(t, ok)
	case <-time.After(3 * time.Second):
		t.Fatal("no keepalive")
	}
}
