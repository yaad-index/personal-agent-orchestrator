package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

type recordingLayer struct {
	mu      sync.Mutex
	inbound []comm.Inbound
}

func (l *recordingLayer) Receive(_ context.Context, in comm.Inbound) (comm.Delivery, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inbound = append(l.inbound, in)
	return comm.Delivery{Message: core.Message{ID: "m1"}, Results: []route.Result{{Agent: in.Target.Value}}}, nil
}

func (l *recordingLayer) React(string, string, string) error { return nil }

func (l *recordingLayer) got() []comm.Inbound {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]comm.Inbound(nil), l.inbound...)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestChatSendsLinesAndPrintsWhatPaoSends(t *testing.T) {
	layer := &recordingLayer{}
	srv, err := local.New(layer, "tok")
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Setenv("PAO_CHAT_TEST_TOKEN", "tok")

	out, errw := &lockedBuffer{}, &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := ChatCmd{URL: ts.URL, TokenEnv: "PAO_CHAT_TEST_TOKEN", To: "a", Sender: "op"}

	pr := newPipe()
	done := make(chan error, 1)
	go func() { done <- c.run(ctx, pr, out, errw) }()

	time.Sleep(100 * time.Millisecond) // let the stream attach
	pr.writeLine("hello agent")
	pr.writeLine("   ") // blank lines are not sent
	pr.writeLine("second")

	require.Eventually(t, func() bool { return len(layer.got()) == 2 }, 3*time.Second, 10*time.Millisecond)
	got := layer.got()
	assert.Equal(t, "hello agent", got[0].Text)
	assert.Equal(t, "op", got[0].Sender)
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "a"}, got[0].Target)
	assert.Equal(t, "second", got[1].Text)

	require.NoError(t, srv.Send(context.Background(), core.Message{ID: "m9", Sender: "a", Text: "pong"}))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "[a]: pong\n") }, 3*time.Second, 10*time.Millisecond)

	pr.close()
	select {
	case err := <-done:
		assert.NoError(t, err, "end of input ends the chat cleanly")
	case <-time.After(3 * time.Second):
		t.Fatal("chat did not end at end of input")
	}
	assert.Empty(t, errw.String())
}

func TestChatByTag(t *testing.T) {
	layer := &recordingLayer{}
	srv, err := local.New(layer, "tok")
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Setenv("PAO_CHAT_TEST_TOKEN", "tok")

	pr := newPipe()
	done := make(chan error, 1)
	go func() {
		done <- ChatCmd{URL: ts.URL, TokenEnv: "PAO_CHAT_TEST_TOKEN", Tag: "team", Sender: "h"}.run(context.Background(), pr, &lockedBuffer{}, &lockedBuffer{})
	}()
	time.Sleep(100 * time.Millisecond)
	pr.writeLine("all")
	require.Eventually(t, func() bool { return len(layer.got()) == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, core.Target{Kind: core.ByTag, Value: "team"}, layer.got()[0].Target)
	pr.close()
	<-done
}

func TestChatNeedsATargetAndAToken(t *testing.T) {
	t.Setenv("PAO_CHAT_TEST_TOKEN", "tok")
	err := ChatCmd{TokenEnv: "PAO_CHAT_TEST_TOKEN"}.run(context.Background(), strings.NewReader(""), &lockedBuffer{}, &lockedBuffer{})
	assert.ErrorContains(t, err, "--to")

	t.Setenv("PAO_CHAT_TEST_EMPTY", "")
	err = ChatCmd{TokenEnv: "PAO_CHAT_TEST_EMPTY", To: "a"}.run(context.Background(), strings.NewReader(""), &lockedBuffer{}, &lockedBuffer{})
	assert.ErrorContains(t, err, "PAO_CHAT_TEST_EMPTY")
}

func TestRunNeedsTheToken(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir)
	t.Setenv("PAO_RUN_TEST_TOKEN", "")
	err := RunCmd{Path: path}.Run()
	require.Error(t, err)
	assert.ErrorContains(t, err, "PAO_RUN_TEST_TOKEN")
}

func TestParseDecision(t *testing.T) {
	for _, tc := range []struct {
		line             string
		decision, id, rs string
		ok               bool
	}{
		{"/allow q1", "allow", "q1", "", true},
		{"/always q12", "always", "q12", "", true},
		{"/deny q3", "deny", "q3", "", true},
		{"/deny q3 not on prod, sorry", "deny", "q3", "not on prod, sorry", true},
		{"/allow", "", "", "", false},
		{"/allow q1 extra", "", "", "", false},
		{"/allow soon", "", "", "", false},
		{"/allow everything now", "", "", "", false},
		{"/unknown q1", "", "", "", false},
		{"allow q1", "", "", "", false},
		{"please /allow q1", "", "", "", false},
		{"/path/to/file is broken", "", "", "", false},
	} {
		d, id, rs, ok := parseDecision(tc.line)
		assert.Equal(t, tc.ok, ok, tc.line)
		assert.Equal(t, tc.decision, d, tc.line)
		assert.Equal(t, tc.id, id, tc.line)
		assert.Equal(t, tc.rs, rs, tc.line)
	}
}
