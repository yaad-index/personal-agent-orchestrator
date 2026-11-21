package daemon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

func TestTheOperatorCreatesListsAndReadsTasksOnTheNode(t *testing.T) {
	n := startNodeWith(t, map[string]string{"a": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "report-api", Env: "PAO_TEST_REPORT_SECRET"}}
	})
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

func TestATaskNoAgentCanRunGetsAOneOffWhichIsKilledWhenTheTaskIsOver(t *testing.T) {
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		body := strings.Replace(goodRecipe("fake"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()

	// Only the recipe's agents carry "temp"; the configured agent does not.
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "Scratch work", Description: "Do it.", Done: "Done.", Tags: []string{"temp"}})
	require.NoError(t, err)
	var got local.TaskJSON
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Assignee != ""
	}, 15*time.Second, 50*time.Millisecond)
	assert.True(t, got.OneOff)
	assert.True(t, strings.HasPrefix(got.Assignee, "fake-"), got.Assignee)
	one := got.Assignee
	assert.True(t, tmuxHasSession(t, n, one), "the one-off runs")

	_, err = n.client.CancelTask(ctx, created.ID, "no longer needed")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Removed
	}, 15*time.Second, 50*time.Millisecond)
	assert.False(t, tmuxHasSession(t, n, one), "and is killed once its task is over")
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "one-off "+one+" removed; its outputs are in harvest ")
	hs, err := n.client.Harvests(ctx, "")
	require.NoError(t, err)
	require.Len(t, hs, 1)
	assert.Equal(t, one, hs[0].Agent)
	// The harvest is in the task's log; no one is sent a report of the kill.
	for {
		select {
		case m := <-n.events:
			assert.NotContains(t, m.Text, hs[0].ID, "the kill is reported to no one")
			continue
		case <-time.After(300 * time.Millisecond):
		}
		break
	}
}

func TestAOneOffIsSpawnedForTheOwnerAndNotForPao(t *testing.T) {
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		body := strings.Replace(goodRecipe("fake"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	// A task owned by an agent with no grants: its one-off would be that agent's
	// spawn, which the matrix refuses.
	id, err := task.NewID(time.Now())
	require.NoError(t, err)
	st := &task.Store{Root: filepath.Join(n.stateDir, "tasks")}
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.Put(task.FolderOpen, task.Task{ID: id, Title: "x", Description: "y", Done: "z", Tags: []string{"temp"}, Owner: "agent:nobody", Status: task.Open, Created: now, Updated: now}))
	require.Eventually(t, func() bool {
		got, err := n.client.Task(context.Background(), id)
		return err == nil && len(got.Log) > 0 && strings.Contains(got.Log[len(got.Log)-1].Text, "a one-off from recipe fake could not be started")
	}, 15*time.Second, 50*time.Millisecond)
	got, err := n.client.Task(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
}

func paneEnv(t *testing.T, n *node, agent string) string {
	t.Helper()
	out, err := osexec.Command("tmux", "-L", n.sock, "display-message", "-p", "-t", "=pao-"+agent+":", "#{pane_pid}").Output()
	require.NoError(t, err)
	b, err := os.ReadFile("/proc/" + strings.TrimSpace(string(out)) + "/environ")
	require.NoError(t, err)
	return strings.ReplaceAll(string(b), "\x00", "\n")
}

func TestATasksSecretIsAFileForItsOneOffAloneAndGoneWithIt(t *testing.T) {
	const value = "sk-TEST-SECRET-VALUE-0451"
	t.Setenv("PAO_TEST_REPORT_SECRET", value)
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		// The recipe carries "team", as the configured agent does: the secret
		// is what sends the task to a one-off.
		body := strings.Replace(goodRecipe("fake"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		body = strings.Replace(body, "  tags: [temp]\n", "  tags: [temp, team]\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "report-api", Env: "PAO_TEST_REPORT_SECRET"}}
		for i := range cfg.Profiles {
			cfg.Profiles[i].Account = "local"
		}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()
	// The configured agent is up and free, and would be preferred to a one-off.
	_, err := n.client.Post(ctx, "human", "hello", "name", "a")
	require.NoError(t, err)
	for m := n.next(t); m.Sender != "a"; m = n.next(t) {
	}

	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "Post the report", Description: "Post it.", Done: "Posted.", Tags: []string{"team"},
		Attachments: task.Attachments{Secret: "report-api"}})
	require.NoError(t, err)
	var got local.TaskJSON
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Assignee != ""
	}, 15*time.Second, 50*time.Millisecond)
	one := got.Assignee
	assert.True(t, strings.HasPrefix(one, "fake-"), "not the configured agent a, though it carries team and is free: %s", one)

	path := filepath.Join(n.stateDir, "run-secrets", one, "report-api")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, value, string(b))
	env := paneEnv(t, n, one)
	assert.Contains(t, env, "PAO_SECRET_REPORT_API="+path+"\n", "the agent is given the path")
	// And its brief names the file, for an agent that has no shell to read a
	// variable with: the fake agent echoes each line it is given.
	for m := n.next(t); m.Sender != one || !strings.Contains(m.Text, "the file "+path); m = n.next(t) {
		assert.NotContains(t, m.Text, value)
	}
	assert.NotContains(t, env, value, "never the value")
	assert.NotContains(t, paneEnv(t, n, "a"), "PAO_SECRET_", "and no other agent is given anything")
	assert.NotContains(t, paneEnv(t, n, "a"), value, "nor inherits the variable the node read it from")

	// The value is in no task file, and in nothing the operator or a channel saw.
	files, err := filepath.Glob(filepath.Join(n.stateDir, "tasks", "*", "*.yaml"))
	require.NoError(t, err)
	for _, f := range files {
		fb, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotContains(t, string(fb), value, f)
	}

	_, err = n.client.CancelTask(ctx, created.ID, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Removed
	}, 15*time.Second, 50*time.Millisecond)
	assert.NoFileExists(t, path, "gone with the agent")
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "its transcript was removed", "and so is its transcript")
	assert.NoDirExists(t, filepath.Join(n.stateDir, "run-secrets", one))
	hs, err := n.client.Harvests(ctx, "")
	require.NoError(t, err)
	require.Len(t, hs, 1)
	assert.NotContains(t, fmt.Sprint(hs[0]), value)
}

func TestLeftoverRunSecretsAreSweptAtStart(t *testing.T) {
	var leftover string
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, _ string, cfg *config.Config) {
		leftover = filepath.Join(cfg.Orchestrator.StateDir, "run-secrets", "tmp-gone", "report-api")
		require.NoError(t, os.MkdirAll(filepath.Dir(leftover), 0o700))
		require.NoError(t, os.WriteFile(leftover, []byte("old value"), 0o600))
	})
	_ = n
	assert.NoFileExists(t, leftover)
}

func TestASecretIsResolvedFromWhatWasReadAtStartOrFromItsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "deploy")
	require.NoError(t, os.WriteFile(f, []byte("file-value"), 0o600))
	o := config.Orchestrator{Secrets: []config.Secret{{Name: "api", Env: "X_API"}, {Name: "empty", Env: "X_EMPTY"}, {Name: "deploy", File: f}, {Name: "gone", File: filepath.Join(dir, "missing")}}}
	t.Setenv("X_API", "not-this")
	resolve := resolveSecret(o, map[string]string{"api": "env-value"})
	v, err := resolve("api")
	require.NoError(t, err)
	assert.Equal(t, "env-value", string(v), "the value read at start, not the environment as it is now")
	v, err = resolve("deploy")
	require.NoError(t, err)
	assert.Equal(t, "file-value", string(v))
	for name, want := range map[string]string{"empty": "variable X_EMPTY is empty", "gone": `secret "gone"`, "nope": `no secret named "nope"`} {
		_, err := resolve(name)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), want, name)
	}
}

func TestASecretIsWipedWhenItsOneOffFailsToStart(t *testing.T) {
	t.Setenv("PAO_TEST_REPORT_SECRET", "sk-WIPED-WHEN-SPAWN-FAILS")
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		// A transcript format the node has no reader for: the spawn is refused
		// after the secret was put in place.
		body := strings.Replace(tempRecipe("@@SCRIPT@@", "broken", "READY>", "no-such-format"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		body = strings.Replace(body, "  tags: [temp]\n", "  tags: [temp, team]\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "broken.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "report-api", Env: "PAO_TEST_REPORT_SECRET"}}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}, Attachments: task.Attachments{Secret: "report-api"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := n.client.Task(ctx, created.ID)
		return err == nil && len(got.Log) > 0 && strings.Contains(got.Log[len(got.Log)-1].Text, "a one-off from recipe broken could not be started")
	}, 15*time.Second, 50*time.Millisecond)
	left, err := os.ReadDir(filepath.Join(n.stateDir, "run-secrets"))
	require.NoError(t, err)
	assert.Empty(t, left, "nothing of the secret is left behind")
}

func TestTaskToolsKnowPaosProxiedToolsAndWhoReachesThem(t *testing.T) {
	ups := []config.MCPUpstream{{Name: "search", URL: "https://x/mcp", Tools: map[string]config.UpstreamTool{"query": {Tags: []string{"research"}}}}}
	g := toolGrants{access.Grant{Subject: access.Agent("bee"), Kind: access.ResourceTool, Tag: "research"}}
	tt := newTaskTools(access.Matrix{Grants: g}, ups)
	assert.True(t, tt.Proxied("search__query"))
	assert.False(t, tt.Proxied("Read"))
	assert.True(t, tt.Reaches("bee", "search__query"))
	assert.False(t, tt.Reaches("wasp", "search__query"), "an agent without the tool's tag")
	assert.False(t, tt.Reaches("bee", "Read"), "a tool pao does not proxy is reached by no grant")
}

type toolGrants []access.Grant

func (g toolGrants) Of(s access.Subject) []access.Grant {
	var out []access.Grant
	for _, x := range g {
		if x.Subject == s {
			out = append(out, x)
		}
	}
	return out
}

func TestAOneOffThatNeverBecomesReadyIsKilledWithNoReport(t *testing.T) {
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		body := strings.Replace(tempRecipe("@@SCRIPT@@", "stuck", "NEVER-READY>", "claude-code"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "stuck.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"temp"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := n.client.Task(ctx, created.ID)
		return err == nil && len(got.Log) > 0 && strings.Contains(got.Log[len(got.Log)-1].Text, "could not be started")
	}, 15*time.Second, 50*time.Millisecond)
	hs, err := n.client.Harvests(ctx, "")
	require.NoError(t, err)
	require.Len(t, hs, 1, "the agent that never became ready was killed, harvesting first")
	for {
		select {
		case m := <-n.events:
			assert.NotContains(t, m.Text, "harvested", "its kill is reported to no one")
			continue
		case <-time.After(300 * time.Millisecond):
		}
		break
	}
}

func TestForgetRemovesOnlyTheAgentsOwnTranscriptAndConfirmsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "temp")
	o := taskOneOffs{mgr: &temp.Manager{Workdirs: &workdir.Provisioner{Root: root}}, quiet: 30 * time.Millisecond, poll: 5 * time.Millisecond}
	workdirOf := filepath.Join(root, "tmp-a")
	harness := claudecode.TranscriptDir(home, workdirOf)

	require.NoError(t, os.MkdirAll(harness, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(harness, "s.jsonl"), []byte("the value"), 0o600))
	require.NoError(t, o.Forget("tmp-a", harness))
	assert.NoDirExists(t, harness, "the harness's directory for the agent is removed")

	err := o.Forget("tmp-a", harness)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no transcript at", "one that is not where its backend keeps it is a failure, said aloud")

	other := filepath.Join(home, "notes")
	require.NoError(t, os.WriteFile(other, []byte("keep"), 0o600))
	err = o.Forget("tmp-a", other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not the agent's own transcript")
	assert.FileExists(t, other, "a path that is not the agent's own is never removed")
	assert.Error(t, o.Forget("tmp-a", claudecode.TranscriptDir(home, filepath.Join(root, "tmp-b"))), "nor another agent's")
	assert.Error(t, o.Forget("tmp-a", ""))

	require.NoError(t, os.MkdirAll(workdirOf, 0o700))
	inside := filepath.Join(workdirOf, "t.jsonl")
	require.NoError(t, os.WriteFile(inside, []byte("the value"), 0o600))
	require.NoError(t, o.Forget("tmp-a", inside))
	assert.NoFileExists(t, inside)
	require.NoError(t, o.Forget("tmp-a", inside), "kept in the working directory, it went with it")
}

func TestATranscriptThatIsNotThereIsToldToTheOperator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PAO_TEST_REPORT_SECRET", "sk-TRANSCRIPT-CHECK")
	no := false
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		// No transcript path: the node locates it where the harness keeps one,
		// under the home directory, where the fake agent never writes.
		body := strings.Replace(goodRecipe("fake"), "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		body = strings.Replace(body, "    path: \"{workdir}/t.jsonl\"\n", "", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "report-api", Env: "PAO_TEST_REPORT_SECRET"}}
	}, func(o *Options) { o.DispatchEvery = 50 * time.Millisecond })
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"temp"}, Attachments: task.Attachments{Secret: "report-api"}})
	require.NoError(t, err)
	var got local.TaskJSON
	require.Eventually(t, func() bool { got, err = n.client.Task(ctx, created.ID); return err == nil && got.Assignee != "" }, 15*time.Second, 50*time.Millisecond)
	_, err = n.client.CancelTask(ctx, created.ID, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { got, err = n.client.Task(ctx, created.ID); return err == nil && got.Removed }, 15*time.Second, 50*time.Millisecond)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "its transcript could NOT be removed: no transcript at")
	for m := n.next(t); !strings.Contains(m.Text, "could not be removed"); m = n.next(t) {
	}
}

func TestATranscriptWrittenAgainAfterTheKillIsRemovedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "temp")
	o := taskOneOffs{mgr: &temp.Manager{Workdirs: &workdir.Provisioner{Root: root}}, quiet: 60 * time.Millisecond, poll: 5 * time.Millisecond}
	harness := claudecode.TranscriptDir(home, filepath.Join(root, "tmp-a"))
	require.NoError(t, os.MkdirAll(harness, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(harness, "s.jsonl"), []byte("the value"), 0o600))
	// The harness writes its last record as it exits, just after the removal.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Twice, 40ms then 30ms apart: the quiet time starts over at each
		// return, so the second, after a quiet time counted from the first
		// removal would have ended, is caught too.
		for _, at := range []time.Duration{40 * time.Millisecond, 30 * time.Millisecond} {
			time.Sleep(at)
			_ = os.MkdirAll(harness, 0o700)
			_ = os.WriteFile(filepath.Join(harness, "last.jsonl"), []byte("summary"), 0o600)
		}
	}()
	require.NoError(t, o.Forget("tmp-a", harness))
	<-done
	assert.NoDirExists(t, harness, "removed again when it came back, and gone at the end")
}

func TestATranscriptThatKeepsComingBackIsAFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "temp")
	o := taskOneOffs{mgr: &temp.Manager{Workdirs: &workdir.Provisioner{Root: root}}, quiet: 30 * time.Millisecond, poll: 2 * time.Millisecond}
	harness := claudecode.TranscriptDir(home, filepath.Join(root, "tmp-a"))
	require.NoError(t, os.MkdirAll(harness, 0o700))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				_ = os.MkdirAll(harness, 0o700)
			}
		}
	}()
	err := o.Forget("tmp-a", harness)
	close(stop)
	<-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kept coming back")
}

func TestATranscriptAlwaysThereStillEndsInAFailureOnTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "temp")
	harness := claudecode.TranscriptDir(home, filepath.Join(root, "tmp-a"))
	require.NoError(t, os.MkdirAll(harness, 0o700))
	info, err := os.Lstat(harness)
	require.NoError(t, err)
	stats := 0
	// A backend that has rewritten it before every single look: it is never
	// seen gone, so only the deadline can end the loop.
	o := taskOneOffs{mgr: &temp.Manager{Workdirs: &workdir.Provisioner{Root: root}}, quiet: 20 * time.Millisecond, poll: time.Millisecond,
		statFn: func(string) (fs.FileInfo, error) { stats++; return info, nil }}
	start := time.Now()
	err = o.Forget("tmp-a", harness)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kept coming back")
	assert.Less(t, time.Since(start), time.Second, "ended by the deadline (80ms here), not by luck")
	assert.Less(t, stats, 200, "and it sleeps a poll between passes rather than spinning")
}
