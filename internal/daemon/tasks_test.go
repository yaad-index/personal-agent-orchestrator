package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
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
	// Dispatch may have noted why it waits (the agent names no account): the
	// task is otherwise what was created.
	got.Log, got.Updated = nil, created.Updated
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

func TestTheOperatorCancelsATaskWhichGoesToTheArchive(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}})
	require.NoError(t, err)

	got, err := n.client.CancelTask(ctx, created.ID, "not needed")
	require.NoError(t, err)
	assert.Equal(t, task.Canceled, got.Status)
	assert.Equal(t, task.FolderArchive, got.Folder)
	assert.FileExists(t, filepath.Join(n.stateDir, "tasks", "archive", created.ID+".yaml"))
	assert.NoFileExists(t, filepath.Join(n.stateDir, "tasks", "open", created.ID+".yaml"))
	require.NotEmpty(t, got.Log)
	assert.Equal(t, "not needed", got.Log[len(got.Log)-1].Text)

	_, err = n.client.CancelTask(ctx, created.ID, "")
	assert.ErrorContains(t, err, "400", "already over")
	_, err = n.client.CancelTask(ctx, "t-20991231-000000-000000", "")
	assert.ErrorContains(t, err, "404")
}

func TestAnOpenTaskIsDispatchedToAnAgentWithHeadroomAndTheAgentIsTold(t *testing.T) {
	no := false
	n := startNodeWith(t, map[string]string{"a": "", "b": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == "a" {
				cfg.Profiles[i].Account = "local"
			}
		}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()

	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "Weekly report", Description: "Write it.", Done: "Posted.", Tags: []string{"team"}})
	require.NoError(t, err)
	var got local.TaskJSON
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Assignee != ""
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, "a", got.Assignee, "b names no account, so it has no headroom")
	assert.Equal(t, task.Assigned, got.Status)

	// The agent was told: the fake agent echoes the brief's first line.
	for {
		m := n.next(t)
		if m.Sender == "a" && strings.Contains(m.Text, "You are assigned task "+created.ID) {
			break
		}
	}
}

func TestTheFleetLeavesOutTheGovernorAndRemoteAgents(t *testing.T) {
	reg := registry.New()
	for _, p := range []core.Profile{{Name: "a", Tags: []string{"team"}, Account: "x"}, {Name: "gov", Tags: []string{"team"}}, {Name: "far", Tags: []string{"team"}, Node: "n2"}} {
		require.NoError(t, reg.Add(p))
	}
	var names []string
	for _, c := range (taskFleet{reg: reg, governor: "gov"}).Candidates() {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"a"}, names)
	assert.Error(t, (taskFleet{reg: reg}).Deliver(context.Background(), "nobody", "x"))
}
