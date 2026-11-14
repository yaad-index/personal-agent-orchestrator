package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentCommandsPostTheirAction(t *testing.T) {
	s := newStubNode(t, noContent)
	f := s.flags(t)
	for action, run := range map[string]func(*bytes.Buffer) error{
		"start":   func(b *bytes.Buffer) error { return agentAction(f, b, "bee", "start") },
		"stop":    func(b *bytes.Buffer) error { return agentAction(f, b, "bee", "stop") },
		"restart": func(b *bytes.Buffer) error { return agentAction(f, b, "bee", "restart") },
	} {
		var out bytes.Buffer
		require.NoError(t, run(&out))
		assert.Equal(t, "POST /v1/agents/bee/"+action, s.path)
		assert.Equal(t, "Bearer tok", s.auth)
		assert.Contains(t, out.String(), "bee: "+action+" done")
	}
}

func TestAnAgentActionThatIsRefusedIsReportedWithItsStatus(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 409, map[string]string{"error": "temporary"})
	})
	err := agentAction(s.flags(t), &bytes.Buffer{}, "tmp", "stop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
}
