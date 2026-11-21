package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

func TestTheOperatorCreatesListsAndReadsTasksOnTheNode(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	ctx := context.Background()

	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{
		Title: "Weekly report", Description: "Write the weekly report.", Done: "It is posted.", Tags: []string{"team"},
		Attachments: task.Attachments{Secret: "report-api"},
	})
	require.NoError(t, err)
	assert.Equal(t, task.Open, created.Status)
	assert.Equal(t, "operator", created.Owner)
	assert.Equal(t, task.FolderOpen, created.Folder)
	assert.FileExists(t, filepath.Join(n.stateDir, "tasks", "open", created.ID+".yaml"), "a task is a file in the node's state")

	_, err = n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"night"}})
	assert.ErrorContains(t, err, "404", "a tag the operator does not hold is refused as not found")
	_, err = n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z"})
	assert.ErrorContains(t, err, "400")
	assert.ErrorContains(t, err, "at least one tag")

	list, err := n.client.Tasks(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, created.ID, list[0].ID)

	got, err := n.client.Task(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created, got)
	_, err = n.client.Task(ctx, "t-20991231-000000-000000")
	assert.ErrorContains(t, err, "404")
	_, err = n.client.Task(ctx, "../grants")
	assert.ErrorContains(t, err, "404")

	// Over MCP the operator sees the same task, and may make one from it.
	op := mcpSession(t, n, token)
	listed := mcpCall[struct{ Tasks []mcpsrv.TaskInfo }](t, op, mcpsrv.ToolListTasks, nil)
	require.Len(t, listed.Tasks, 1)
	assert.Equal(t, created.ID, listed.Tasks[0].ID)
	child := mcpCall[struct{ Task mcpsrv.TaskInfo }](t, op, mcpsrv.ToolCreateTask, map[string]any{
		"title": "Review the report", "description": "Review it.", "done": "Reviewed.", "tags": []string{"team"}, "parent": created.ID,
	})
	assert.Equal(t, created.ID, child.Task.Parent)

	// The file holds the secret's name, which is all a task ever holds.
	b, err := os.ReadFile(filepath.Join(n.stateDir, "tasks", "open", created.ID+".yaml"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(b), "secret: report-api"))
}
