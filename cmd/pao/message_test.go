package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// messageLayer answers as the node's layer would: a /pao command is run as one,
// anything else is routed to the agents the node has.
type messageLayer struct {
	agents  map[string]error // agent name -> its delivery error
	mu      sync.Mutex
	inbound []comm.Inbound
}

func (l *messageLayer) ReceiveAsOperator(_ context.Context, in comm.Inbound) (comm.Delivery, error) {
	l.mu.Lock()
	l.inbound = append(l.inbound, in)
	l.mu.Unlock()
	derr, ok := l.agents[in.Target.Value]
	if !ok {
		return comm.Delivery{}, route.NoMatch(in.Target)
	}
	return comm.Delivery{Message: core.Message{ID: "m7"}, Results: []route.Result{{Agent: in.Target.Value, Err: derr}}}, nil
}

func (l *messageLayer) CommandAsOperator(_ context.Context, in comm.Inbound) (string, *comm.Delivery, bool, error) {
	if !strings.HasPrefix(in.Text, "/pao") {
		return "", nil, false, nil
	}
	return "pao answers " + in.Text, nil, true, nil
}

func (l *messageLayer) React(string, string, string) error { return nil }

func (l *messageLayer) got() []comm.Inbound {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]comm.Inbound(nil), l.inbound...)
}

// messageNode serves the real local channel over layer and records each request.
func messageNode(t *testing.T, layer *messageLayer) (NodeFlags, func() []string) {
	t.Helper()
	srv, err := local.New(layer, "tok")
	require.NoError(t, err)
	var mu sync.Mutex
	var reqs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		mu.Unlock()
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_MESSAGE_TEST_TOKEN", "tok")
	return NodeFlags{URL: ts.URL, TokenEnv: "PAO_MESSAGE_TEST_TOKEN"}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reqs...)
	}
}

func TestMessageSendsOnceAsTheOperatorAndPrintsTheID(t *testing.T) {
	layer := &messageLayer{agents: map[string]error{"bee": nil}}
	f, reqs := messageNode(t, layer)
	var out bytes.Buffer
	require.NoError(t, MessageCmd{NodeFlags: f, Agent: "bee", Text: "build the docs"}.run(&out))

	assert.Equal(t, "m7\n", out.String())
	assert.Equal(t, []string{"POST /v1/messages"}, reqs(), "one send, and no stream is opened")
	got := layer.got()
	require.Len(t, got, 1)
	assert.Equal(t, "build the docs", got[0].Text)
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, got[0].Target)
	assert.Equal(t, local.Source, got[0].Source)
}

func TestMessageToAnUnknownAgentFailsAndPrintsNothing(t *testing.T) {
	layer := &messageLayer{agents: map[string]error{"bee": nil}}
	f, _ := messageNode(t, layer)
	var out bytes.Buffer
	err := MessageCmd{NodeFlags: f, Agent: "wasp", Text: "hello"}.run(&out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), `"wasp"`)
	assert.Empty(t, out.String())
}

func TestMessageWhoseDeliveryFailedPrintsTheIDAndFails(t *testing.T) {
	layer := &messageLayer{agents: map[string]error{"bee": errors.New("agent did not start")}}
	f, _ := messageNode(t, layer)
	var out bytes.Buffer
	err := MessageCmd{NodeFlags: f, Agent: "bee", Text: "hello"}.run(&out)
	require.Error(t, err)
	assert.Equal(t, "bee: agent did not start", err.Error())
	assert.Equal(t, "m7\n", out.String(), "the message was stored, so its id is still printed")
}

func TestMessageThatIsACommandPrintsTheAnswerAndRoutesNothing(t *testing.T) {
	layer := &messageLayer{agents: map[string]error{"bee": nil}}
	f, _ := messageNode(t, layer)
	var out bytes.Buffer
	require.NoError(t, MessageCmd{NodeFlags: f, Agent: "bee", Text: "/pao help"}.run(&out))
	assert.Equal(t, "pao answers /pao help\n", out.String())
	assert.Empty(t, layer.got())
}

func TestMessageToAnUnreachableNodeFails(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	t.Setenv("PAO_MESSAGE_TEST_TOKEN", "tok")
	var out bytes.Buffer
	err := MessageCmd{NodeFlags: NodeFlags{URL: url, TokenEnv: "PAO_MESSAGE_TEST_TOKEN"}, Agent: "bee", Text: "hello"}.run(&out)
	require.Error(t, err)
	assert.Empty(t, out.String())
}

func TestMessageNeedsTheToken(t *testing.T) {
	t.Setenv("PAO_MESSAGE_TEST_TOKEN", "")
	err := MessageCmd{NodeFlags: NodeFlags{URL: "http://127.0.0.1:1", TokenEnv: "PAO_MESSAGE_TEST_TOKEN"}, Agent: "bee", Text: "hello"}.run(&bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PAO_MESSAGE_TEST_TOKEN")
}
