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

func TestBudgetShowsEachAccountsStateFigureAndHeadroom(t *testing.T) {
	at := time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []local.BudgetJSON{
			{Account: "local", State: "unmetered", Since: at, Headroom: true},
			{Account: "sub", State: "readable", Week: &local.BudgetWindowJSON{Left: 93}, FiveHour: &local.BudgetWindowJSON{Left: 54}, ReadAt: at, Since: at, Headroom: true},
			{Account: "work", State: "fault", Fault: "usage endpoint refused the login's token (HTTP 401)", Since: at},
			{Account: "busy", State: "readable", Week: &local.BudgetWindowJSON{Left: 80}, FiveHour: &local.BudgetWindowJSON{Left: 40}, ReadAt: at, Since: at, Headroom: true,
				Retrying: "usage endpoint answered HTTP 429", RetryAt: at.Add(5 * time.Minute)},
			{Account: "gone", State: "fault", Fault: "usage endpoint answered HTTP 503", Since: at, RetryAt: at.Add(10 * time.Minute)},
		})
	})
	var out bytes.Buffer
	require.NoError(t, BudgetCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/budget", s.path)
	assert.Regexp(t, `local\s+unmetered\s+no budget\s+headroom`, out.String())
	assert.Regexp(t, `sub\s+readable\s+week 93% left\s+five-hour 54% left\s+read 2026-11-21T18:00:00Z\s+headroom`, out.String())
	assert.Regexp(t, `work\s+fault\s+FAULT since 2026-11-21T18:00:00Z\s+usage endpoint refused the login's token \(HTTP 401\)\s+no headroom`, out.String())
	assert.Contains(t, out.String(), "read 2026-11-21T18:00:00Z (last read failed: usage endpoint answered HTTP 429; again at 2026-11-21T18:05:00Z)")
	assert.Regexp(t, `gone\s+fault\s+FAULT since 2026-11-21T18:00:00Z\s+usage endpoint answered HTTP 503\s+again at 2026-11-21T18:10:00Z\s+no headroom`, out.String())
}

func TestBudgetWithNoAccountsSaysSo(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, []local.BudgetJSON{}) })
	var out bytes.Buffer
	require.NoError(t, BudgetCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "no accounts configured\n", out.String())
}
