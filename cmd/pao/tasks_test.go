package main

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

var sampleTask = local.TaskJSON{Folder: "open", Task: task.Task{
	ID: "t-20261121-183000-abcdef", Title: "Weekly report", Description: "Write it.", Done: "Posted.", Tags: []string{"reports", "team"},
	Owner: "operator", Status: task.Open, Attachments: task.Attachments{Secret: "report-api"},
	Created: time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC), Updated: time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC),
}}

func TestTasksListShowsEachTask(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, []local.TaskJSON{sampleTask}) })
	var out bytes.Buffer
	require.NoError(t, TasksListCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/tasks", s.path)
	assert.Regexp(t, `t-20261121-183000-abcdef\s+open\s+open\s+reports,team\s+-\s+Weekly report`, out.String())
}

func TestTasksListWithNoneSaysSo(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, []local.TaskJSON{}) })
	var out bytes.Buffer
	require.NoError(t, TasksListCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "no tasks\n", out.String())
}

func TestTasksShowPrintsTheTask(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, sampleTask) })
	var out bytes.Buffer
	require.NoError(t, TasksShowCmd{NodeFlags: s.flags(t), ID: sampleTask.ID}.run(&out))
	assert.Equal(t, "GET /v1/tasks/"+sampleTask.ID, s.path)
	assert.Contains(t, out.String(), "secret:      report-api")
	assert.Contains(t, out.String(), "Write it.\n\ndone when: Posted.")
	assert.NotContains(t, out.String(), "assignee:", "an empty field is left out")
}

func TestTasksCreateSendsTheTask(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, sampleTask) })
	var out bytes.Buffer
	require.NoError(t, TasksCreateCmd{NodeFlags: s.flags(t), Title: "Weekly report", Description: "Write it.", Done: "Posted.", Tag: []string{"reports", "team"}, Secret: "report-api", Tool: []string{"Read"}}.run(&out))
	assert.Equal(t, "POST /v1/tasks", s.path)
	assert.JSONEq(t, `{"title":"Weekly report","description":"Write it.","done":"Posted.","tags":["reports","team"],"attachments":{"tools":["Read"],"secret":"report-api"}}`, s.body)
	assert.Equal(t, sampleTask.ID+"\n", out.String())
}
