package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

func TestChannelDefaultSetsAndSaysWhatItReplacedAndMoved(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.DefaultChangeJSON{Channel: "chat", Agent: "bee", Previous: "wasp", Left: "other"})
	})
	var out bytes.Buffer
	require.NoError(t, ChannelDefaultCmd{NodeFlags: s.flags(t), Channel: "chat", Agent: "bee"}.run(&out))
	assert.Equal(t, "PUT /v1/channels/chat/default", s.path)
	assert.JSONEq(t, `{"agent":"bee"}`, s.body)
	assert.Equal(t, "chat: default agent bee (was wasp); bee is no longer the default of other\n", out.String())
}

func TestChannelDefaultClears(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.DefaultChangeJSON{Channel: "chat", Previous: "bee"})
	})
	var out bytes.Buffer
	require.NoError(t, ChannelDefaultCmd{NodeFlags: s.flags(t), Channel: "chat", Clear: true}.run(&out))
	assert.Equal(t, "DELETE /v1/channels/chat/default", s.path)
	assert.Equal(t, "chat: no default agent (was bee)\n", out.String())
}

func TestChannelDefaultNeedsAnAgentOrClearButNotBoth(t *testing.T) {
	f := NodeFlags{}
	assert.Error(t, ChannelDefaultCmd{NodeFlags: f, Channel: "chat"}.run(&bytes.Buffer{}))
	assert.Error(t, ChannelDefaultCmd{NodeFlags: f, Channel: "chat", Agent: "bee", Clear: true}.run(&bytes.Buffer{}))
}

func TestChannelDefaultsListsOneRowEach(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.DefaultsJSON{Version: 2, Defaults: []local.DefaultJSON{{Channel: "chat", Agent: "bee"}, {Channel: "other", Agent: "wasp"}}})
	})
	var out bytes.Buffer
	require.NoError(t, ChannelDefaultsCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/channels/defaults", s.path)
	assert.Equal(t, "chat\tbee\nother\twasp\n", out.String())
}

func TestARefusedChannelDefaultIsReportedWithItsStatus(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": `no agent "nobody"`})
	})
	err := ChannelDefaultCmd{NodeFlags: s.flags(t), Channel: "chat", Agent: "nobody"}.run(&bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `404: no agent "nobody"`)
}
