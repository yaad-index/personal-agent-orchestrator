package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

func TestBindingsAddPostsTheBindingAndSaysWhatWasSet(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.StandingJSON{Agent: "a", Stream: "stdout", To: "group:team", Level: "log", From: "runtime"})
	})
	var out bytes.Buffer
	require.NoError(t, StandingAddCmd{NodeFlags: s.flags(t), Agent: "a", To: "group:team", Stream: "stdout", Level: "log", Prefix: "A:"}.run(&out))
	assert.Equal(t, "POST /v1/bindings", s.path)
	assert.JSONEq(t, `{"agent":"a","stream":"stdout","to":"group:team","level":"log","prefix":"A:"}`, s.body)
	assert.Equal(t, "group:team follows a's stdout at level log\n", out.String())
}

func TestBindingsRemoveDeletesByQuery(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.StandingJSON{Agent: "a", Stream: "stderr", To: "private:alice", From: "runtime"})
	})
	var out bytes.Buffer
	require.NoError(t, StandingRemoveCmd{NodeFlags: s.flags(t), Agent: "a", To: "private:alice", Stream: "stderr"}.run(&out))
	assert.Equal(t, "DELETE /v1/bindings?agent=a&stream=stderr&to=private%3Aalice", s.path)
	assert.Equal(t, "private:alice no longer follows a's stderr\n", out.String())
}

func TestBindingsListShowsWhereEachWasSet(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.StandingListJSON{Bindings: []local.StandingJSON{
			{Agent: "a", Stream: "stdout", To: "group:team", Level: "2", From: "config"},
			{Agent: "a", Stream: "stderr", To: "private:alice", From: "runtime"},
		}})
	})
	var out bytes.Buffer
	require.NoError(t, StandingListCmd{NodeFlags: s.flags(t), Agent: "a"}.run(&out))
	assert.Equal(t, "GET /v1/bindings?agent=a", s.path)
	assert.Equal(t, "AGENT  STREAM  DESTINATION    LEVEL  PREFIX  SET IN\n"+
		"a      stdout  group:team     2      -       config\n"+
		"a      stderr  private:alice  -      -       runtime\n", out.String())

	s = newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.StandingListJSON{})
	})
	out.Reset()
	require.NoError(t, StandingListCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/bindings", s.path)
	assert.Equal(t, "no standing bindings\n", out.String())
}
