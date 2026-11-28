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

func TestRejectedListsOneQuotedRowPerEntry(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []local.RejectedJSON{{
			Channel: "chat", Sender: "1\n2", Name: "Eve\t\x1b[2J", Kind: "message", Reason: "no subject", Count: 4,
			Last: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), Payload: "line one\nforged\trow\x1b[2J",
		}})
	})
	var out bytes.Buffer
	require.NoError(t, RejectedCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/inbound/rejected", s.path)
	assert.Equal(t, "2026-11-16T01:02:03Z\tchat\t\"1\\n2\"\t\"Eve\\t\\x1b[2J\"\tmessage\tx4\t\"no subject\"\t\"line one\\nforged\\trow\\x1b[2J\"\n", out.String(),
		"stranger content cannot start a row of its own or send a control sequence")
}

func TestRejectedReportsARefusal(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 401, map[string]string{"error": "no"}) })
	err := RejectedCmd{NodeFlags: s.flags(t)}.run(&bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}
