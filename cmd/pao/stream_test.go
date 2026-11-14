package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noContent(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func TestStreamBindsAnAgentsOutputToAChannelAtALevel(t *testing.T) {
	s := newStubNode(t, noContent)
	var out bytes.Buffer
	require.NoError(t, StreamCmd{NodeFlags: s.flags(t), Agent: "bee", Channel: "chat", Level: 3}.run(&out))
	assert.Equal(t, "PUT /v1/stream/bee", s.path)
	assert.JSONEq(t, `{"channel":"chat","level":3}`, s.body)
	assert.Equal(t, "Bearer tok", s.auth)
	assert.Contains(t, out.String(), "bee: output goes to chat")
}

func TestStreamClearSendsAnEmptyChannel(t *testing.T) {
	s := newStubNode(t, noContent)
	var out bytes.Buffer
	require.NoError(t, StreamCmd{NodeFlags: s.flags(t), Agent: "bee", Clear: true}.run(&out))
	assert.JSONEq(t, `{"channel":""}`, s.body)
	assert.Contains(t, out.String(), "every channel")
}

func TestStreamNeedsExactlyOneOfChannelAndClear(t *testing.T) {
	s := newStubNode(t, noContent)
	var out bytes.Buffer
	require.Error(t, StreamCmd{NodeFlags: s.flags(t), Agent: "bee"}.run(&out))
	require.Error(t, StreamCmd{NodeFlags: s.flags(t), Agent: "bee", Channel: "chat", Clear: true}.run(&out))
	assert.Empty(t, s.path, "nothing was sent")
}

func TestViewCommandsSendTheirAction(t *testing.T) {
	s := newStubNode(t, noContent)
	f := s.flags(t)
	var out bytes.Buffer

	require.NoError(t, ViewPauseCmd{NodeFlags: f, Agent: "bee"}.run(&out))
	assert.Equal(t, "POST /v1/view/bee", s.path)
	assert.JSONEq(t, `{"action":"pause"}`, s.body)
	require.NoError(t, ViewResumeCmd{NodeFlags: f, Agent: "bee"}.run(&out))
	assert.JSONEq(t, `{"action":"resume"}`, s.body)
	require.NoError(t, ViewBacktrackCmd{NodeFlags: f, Agent: "bee", Level: 3, Entries: 5}.run(&out))
	assert.JSONEq(t, `{"action":"backtrack","level":3,"entries":5}`, s.body)
}

func TestAViewRefusalIsReportedWithItsStatus(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 404, map[string]string{"error": "no agent"}) })
	err := ViewPauseCmd{NodeFlags: s.flags(t), Agent: "ghost"}.run(&bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
