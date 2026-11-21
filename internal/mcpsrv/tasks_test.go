package mcpsrv

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

type fakeTasks struct {
	calls *[]string
	news  *[]task.New
	err   error
}

func (f fakeTasks) Create(_ context.Context, who access.Subject, n task.New) (task.Task, error) {
	*f.calls = append(*f.calls, "create "+who.String())
	*f.news = append(*f.news, n)
	if f.err != nil {
		return task.Task{}, f.err
	}
	return task.Task{ID: "t-20261121-183000-abcdef", Title: n.Title, Tags: n.Tags, Owner: who.String(), Status: task.Open}, nil
}

func (f fakeTasks) List(_ context.Context, who access.Subject) ([]task.Entry, error) {
	*f.calls = append(*f.calls, "list "+who.String())
	return []task.Entry{{Task: task.Task{ID: "t-20261121-183000-abcdef", Title: "x"}, Folder: task.FolderArchive}}, f.err
}

func (f fakeTasks) Get(_ context.Context, who access.Subject, id string) (task.Entry, error) {
	*f.calls = append(*f.calls, "get "+who.String()+" "+id)
	if f.err != nil {
		return task.Entry{}, f.err
	}
	return task.Entry{Task: task.Task{ID: id, Title: "x"}, Folder: task.FolderOpen}, nil
}

func newFakeTasks() fakeTasks { return fakeTasks{calls: &[]string{}, news: &[]task.New{}} }

func TestTheTaskToolsAreOfferedWithTasksToEveryoneButTheGovernor(t *testing.T) {
	res := staticResolver{"g": access.Agent("governor"), "b": access.Agent("boss"), "op": access.Operator}
	url := serve(t, sampleOps, res, WithAuthorizer(access.Matrix{Grants: grantList{governorMayPropose}}), WithGovernor("governor"), WithTasks(newFakeTasks()))
	for _, who := range []string{"b", "op"} {
		got := listed(t, connect(t, url, who))
		for _, name := range TaskTools {
			assert.Contains(t, got, name, who)
		}
	}
	assert.ElementsMatch(t, []string{"list_agents", "list_recipes", ToolPropose}, slices.Collect(maps.Keys(listed(t, connect(t, url, "g")))), "the governor runs no work")

	url = serve(t, sampleOps, res)
	for _, name := range TaskTools {
		assert.NotContains(t, listed(t, connect(t, url, "b")), name, "no task service, no task tools")
	}
}

func TestTheTaskToolsCallTheServiceAsTheResolvedSubject(t *testing.T) {
	ft := newFakeTasks()
	url := serve(t, sampleOps, staticResolver{"b": access.Agent("boss")}, WithTasks(ft))
	cs := connect(t, url, "b")

	out := structured[taskOutput](t, cs, ToolCreateTask, map[string]any{
		"title": "Weekly report", "description": "Write it.", "done": "Posted.", "tags": []string{"reports"},
		"how": "skill: report", "parent": "t-20261121-000000-000000", "tools": []string{"Read"}, "scripts": []string{"s.sh"}, "secret": "report-api",
	})
	assert.Equal(t, "agent:boss", out.Task.Owner)
	assert.Equal(t, task.FolderOpen, out.Task.Folder)
	assert.Equal(t, []task.New{{Title: "Weekly report", Description: "Write it.", Done: "Posted.", How: "skill: report", Tags: []string{"reports"}, Parent: "t-20261121-000000-000000",
		Attachments: task.Attachments{Tools: []string{"Read"}, Scripts: []string{"s.sh"}, Secret: "report-api"}}}, *ft.news)

	list := structured[listTasksOutput](t, cs, ToolListTasks, nil)
	require.Len(t, list.Tasks, 1)
	assert.Equal(t, task.FolderArchive, list.Tasks[0].Folder)
	got := structured[taskOutput](t, cs, ToolGetTask, map[string]any{"id": "t-20261121-183000-abcdef"})
	assert.Equal(t, "t-20261121-183000-abcdef", got.Task.ID)
	assert.Equal(t, []string{"create agent:boss", "list agent:boss", "get agent:boss t-20261121-183000-abcdef"}, *ft.calls)

	isErr, text := toolText(t, cs, ToolGetTask, map[string]any{"id": ""})
	assert.True(t, isErr)
	assert.Contains(t, text, "an id is required")
}

func TestATaskServiceErrorIsAToolErrorAndADenialStaysADenial(t *testing.T) {
	ft := newFakeTasks()
	ft.err = errors.Join(task.ErrDenied, errors.New("operator said no"))
	url := serve(t, sampleOps, staticResolver{"b": access.Agent("boss")}, WithTasks(ft))
	cs := connect(t, url, "b")
	isErr, text := toolText(t, cs, ToolCreateTask, map[string]any{"title": "x", "description": "y", "done": "z", "tags": []string{"r"}})
	assert.True(t, isErr)
	assert.Contains(t, text, "operator said no")
	assert.True(t, errors.Is(taskError(ft.err), ErrDenied))
}

func TestTheGateRulesReadTaskReadsAsLowRiskAndLetTheTaskToolsThroughTheHook(t *testing.T) {
	rules := GateRules(DefaultServerName)
	assert.Contains(t, rules, gate.Rule{Tool: task.ActionList, Risk: gate.RiskRead})
	assert.Contains(t, rules, gate.Rule{Tool: task.ActionRead, Risk: gate.RiskRead})
	for _, r := range rules {
		assert.NotEqual(t, task.ActionCreate, r.Tool, "creating a task is not classified, so it is asked")
	}
	for _, name := range TaskTools {
		assert.Contains(t, rules, gate.Rule{Tool: "mcp__pao__" + name, Effect: gate.EffectAllow}, name)
	}
}
