package main

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

func TestIdentityCommandsCallTheirRoutes(t *testing.T) {
	s := newStubNode(t, noContent)
	f := s.flags(t)
	for _, c := range []struct {
		run  func(*bytes.Buffer) error
		path string
		out  string
	}{
		{func(b *bytes.Buffer) error { return SubjectAddCmd{NodeFlags: f, Name: "alice"}.run(b) }, "POST /v1/identity/subjects", "subject alice added"},
		{func(b *bytes.Buffer) error { return SubjectRemoveCmd{NodeFlags: f, Name: "alice"}.run(b) }, "DELETE /v1/identity/subjects/alice", "alice removed with its bindings"},
		{func(b *bytes.Buffer) error {
			return BindCmd{NodeFlags: f, Channel: "chat", Sender: "7", Subject: "alice"}.run(b)
		}, "POST /v1/identity/bindings", "chat 7 bound to alice"},
		{func(b *bytes.Buffer) error { return RevokeCmd{NodeFlags: f, Channel: "chat", Sender: "7"}.run(b) }, "DELETE /v1/identity/bindings?channel=chat&sender=7", "chat 7 unbound"},
	} {
		var out bytes.Buffer
		require.NoError(t, c.run(&out))
		assert.Equal(t, c.path, s.path)
		assert.Equal(t, "Bearer tok", s.auth)
		assert.Contains(t, out.String(), c.out)
	}
}

func TestIdentityListsPrintOneRowEach(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/identity/subjects" {
			writeJSON(w, 200, []local.SubjectJSON{{Name: "alice", Created: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)}})
			return
		}
		writeJSON(w, 200, []local.BindingJSON{{Channel: "chat", Sender: "7", Subject: "alice"}})
	})
	var out bytes.Buffer
	require.NoError(t, SubjectsCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "alice\t2026-11-16T01:02:03Z\n", out.String())

	out.Reset()
	require.NoError(t, BindingsCmd{NodeFlags: s.flags(t), Subject: "alice"}.run(&out))
	assert.Equal(t, "chat\t7\talice\n", out.String())
	assert.Equal(t, "GET /v1/identity/bindings?subject=alice", s.path)
}

func TestARefusedIdentityCommandIsReportedWithItsStatus(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 409, map[string]string{"error": "sender already bound"})
	})
	err := BindCmd{NodeFlags: s.flags(t), Channel: "chat", Sender: "7", Subject: "alice"}.run(&bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
}
