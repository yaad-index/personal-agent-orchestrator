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
			writeJSON(w, 200, []local.SubjectJSON{{Name: "alice", Created: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), Operator: true}})
			return
		}
		writeJSON(w, 200, []local.BindingJSON{{Channel: "chat", Sender: "7", Subject: "alice"}})
	})
	var out bytes.Buffer
	require.NoError(t, SubjectsCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "alice\t2026-11-16T01:02:03Z\toperator\n", out.String())

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

func TestInviteShowsTheSecretOnceAndListsNeverDo(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, 201, local.InvitationJSON{ID: "ab12", Subject: "alice", Secret: "AAAA-BBBB", Expires: time.Date(2026, 11, 16, 9, 15, 0, 0, time.UTC)})
			return
		}
		writeJSON(w, 200, []local.InvitationJSON{{ID: "ab12", Subject: "alice", Expires: time.Date(2026, 11, 16, 9, 15, 0, 0, time.UTC)}})
	})
	var out bytes.Buffer
	require.NoError(t, InviteCmd{NodeFlags: s.flags(t), Subject: "alice", TTL: 2 * time.Minute}.run(&out))
	assert.Equal(t, "POST /v1/identity/invitations", s.path)
	assert.JSONEq(t, `{"subject":"alice","ttl_seconds":120}`, s.body)
	assert.Equal(t, "invitation ab12 for alice, valid until 2026-11-16T09:15:00Z\nsecret (shown once): AAAA-BBBB\n", out.String())

	out.Reset()
	require.NoError(t, InvitesCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "ab12\talice\t2026-11-16T09:15:00Z\n", out.String())

	out.Reset()
	s2 := newStubNode(t, noContent)
	require.NoError(t, UninviteCmd{NodeFlags: s2.flags(t), ID: "ab12"}.run(&out))
	assert.Equal(t, "DELETE /v1/identity/invitations/ab12", s2.path)
	assert.Contains(t, out.String(), "ab12 revoked")
}

func TestOperatorFlagsAndClears(t *testing.T) {
	s := newStubNode(t, noContent)
	var out bytes.Buffer
	require.NoError(t, OperatorCmd{NodeFlags: s.flags(t), Name: "alice"}.run(&out))
	assert.Equal(t, "PUT /v1/identity/subjects/alice/operator", s.path)
	assert.JSONEq(t, `{"operator":true}`, s.body)
	assert.Contains(t, out.String(), "alice now acts as the operator")

	out.Reset()
	require.NoError(t, OperatorCmd{NodeFlags: s.flags(t), Name: "alice", Off: true}.run(&out))
	assert.JSONEq(t, `{"operator":false}`, s.body)
	assert.Contains(t, out.String(), "alice is no longer an operator")
}
