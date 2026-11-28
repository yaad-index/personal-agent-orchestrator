package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogCommandsPauseAndResumeAnAgentsLogChannel(t *testing.T) {
	s := newStubNode(t, noContent)
	var out bytes.Buffer
	require.NoError(t, pauseLog(s.flags(t), &out, "bee", true))
	assert.Equal(t, "PUT /v1/agents/bee/log", s.path)
	assert.JSONEq(t, `{"paused":true}`, s.body)
	assert.Equal(t, "Bearer tok", s.auth)
	assert.Contains(t, out.String(), "bee's log channel is paused")
	require.NoError(t, pauseLog(s.flags(t), &out, "bee", false))
	assert.JSONEq(t, `{"paused":false}`, s.body)
	assert.Contains(t, out.String(), "bee's log channel is resumed")

	refused := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": `agent "ghost" has no log channel`})
	})
	err := pauseLog(refused.flags(t), &bytes.Buffer{}, "ghost", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no log channel")
}
