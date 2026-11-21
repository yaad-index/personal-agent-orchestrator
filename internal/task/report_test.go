package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

// assignedTask creates a task owned by the operator and assigns it to agent, as
// dispatch will.
func assignedTask(t *testing.T, s *Service, agent string) Task {
	t.Helper()
	tk, err := s.Create(context.Background(), access.Operator, newTask("r"))
	require.NoError(t, err)
	tk.Assignee, tk.Status = agent, Assigned
	require.NoError(t, s.Store.Put(FolderOpen, tk))
	return tk
}

func reportGrants() grants {
	return grants{"operator": {tagGrant("operator", "r")}, "agent:bee": {tagGrant("agent:bee", "r")}, "agent:wasp": {tagGrant("agent:wasp", "r")}}
}

func TestTheAssigneeReportsAndPaoRecordsItInTheLog(t *testing.T) {
	s, fg := service(t, reportGrants())
	tk := assignedTask(t, s, "bee")
	ctx := context.Background()
	fg.asked = nil

	got, err := s.Report(ctx, access.Agent("bee"), tk.ID, Report{Status: Running, Comment: "starting", Log: "fetched the data"})
	require.NoError(t, err)
	assert.Equal(t, Running, got.Status)
	require.Len(t, got.Log, 3)
	assert.Equal(t, LogEntry{At: at, By: "agent:bee", Kind: EntryComment, Text: "starting"}, got.Log[0])
	assert.Equal(t, LogEntry{At: at, By: "agent:bee", Kind: EntryLog, Text: "fetched the data"}, got.Log[1])
	assert.Equal(t, LogEntry{At: at, By: "agent:bee", Kind: EntryStatus, Status: Running}, got.Log[2])
	require.Len(t, fg.asked, 1)
	assert.Equal(t, ActionReport, fg.asked[0].Tool)

	again, err := s.Report(ctx, access.Agent("bee"), tk.ID, Report{Status: Running})
	require.NoError(t, err)
	assert.Len(t, again.Log, 3, "the same status again adds nothing")

	e, err := s.Get(ctx, access.Operator, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, FolderOpen, e.Folder)
	assert.Equal(t, again, e.Task, "what was reported is in the file")
}

func TestDoneOrFailedIsTheAgentsWordAndArchivesTheTask(t *testing.T) {
	for _, st := range []Status{Done, Failed} {
		s, _ := service(t, reportGrants())
		tk := assignedTask(t, s, "bee")
		ctx := context.Background()
		got, err := s.Report(ctx, access.Agent("bee"), tk.ID, Report{Status: st, Comment: "the report is posted"})
		require.NoError(t, err, st)
		assert.Equal(t, st, got.Status)
		e, err := s.Get(ctx, access.Operator, tk.ID)
		require.NoError(t, err)
		assert.Equal(t, FolderArchive, e.Folder, st)
		assert.NoFileExists(t, filepath.Join(s.Store.Root, FolderOpen, tk.ID+".yaml"))

		_, err = s.Report(ctx, access.Agent("bee"), tk.ID, Report{Comment: "one more thing"})
		assert.ErrorIs(t, err, ErrBadRequest, "an archived task takes no more reports")
	}
}

func TestOnlyTheAssigneeReports(t *testing.T) {
	g := reportGrants()
	g["agent:ant"] = []access.Grant{tagGrant("agent:ant", "other")}
	s, fg := service(t, g)
	tk := assignedTask(t, s, "bee")
	ctx := context.Background()
	fg.asked = nil
	_, err := s.Report(ctx, access.Agent("wasp"), tk.ID, Report{Status: Done})
	assert.ErrorIs(t, err, ErrNotAssignee, "an agent that sees it but is not working it")
	_, err = s.Report(ctx, access.Operator, tk.ID, Report{Status: Done})
	assert.ErrorIs(t, err, ErrNotAssignee, "nor the operator: pao never sets done for the agent")
	_, err = s.Report(ctx, access.Agent("ant"), tk.ID, Report{Comment: "x"})
	assert.ErrorIs(t, err, ErrNotFound, "one that cannot see it is told it does not exist")
	assert.Empty(t, fg.asked)

	unassigned, err := s.Create(ctx, access.Operator, newTask("r"))
	require.NoError(t, err)
	_, err = s.Report(ctx, access.Agent("bee"), unassigned.ID, Report{Status: Running})
	assert.ErrorIs(t, err, ErrNotAssignee, "an open task has no one to report on it")
}

func TestTheAssigneeReportsEvenAfterLosingTheTasksTags(t *testing.T) {
	g := reportGrants()
	s, _ := service(t, g)
	tk := assignedTask(t, s, "bee")
	g["agent:bee"] = nil
	_, err := s.Report(context.Background(), access.Agent("bee"), tk.ID, Report{Log: "still working"})
	assert.NoError(t, err)
}

func TestAReportIsCheckedBeforeAnythingIsWritten(t *testing.T) {
	s, fg := service(t, reportGrants())
	tk := assignedTask(t, s, "bee")
	ctx := context.Background()
	for name, c := range map[string]struct {
		r    Report
		want string
	}{
		"empty":         {Report{Comment: "  "}, "a status, a comment or a log line"},
		"open":          {Report{Status: Open}, `not "open"`},
		"assigned":      {Report{Status: Assigned}, "assigned"},
		"canceled":      {Report{Status: Canceled}, "canceled"},
		"unknown":       {Report{Status: "finished"}, "finished"},
		"long comment":  {Report{Comment: strings.Repeat("x", MaxEntry+1)}, "at most"},
		"long log line": {Report{Log: strings.Repeat("x", MaxEntry+1)}, "at most"},
	} {
		_, err := s.Report(ctx, access.Agent("bee"), tk.ID, c.r)
		require.ErrorIs(t, err, ErrBadRequest, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
	fg.deny = true
	_, err := s.Report(ctx, access.Agent("bee"), tk.ID, Report{Status: Done})
	assert.ErrorIs(t, err, ErrDenied)
	e, err := s.Get(ctx, access.Operator, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, Assigned, e.Task.Status, "nothing refused was written")
	assert.Empty(t, e.Task.Log)
}

func TestTheLogKeepsTheNewestEntriesAndCountsTheRest(t *testing.T) {
	s, _ := service(t, reportGrants())
	tk := assignedTask(t, s, "bee")
	ctx := context.Background()
	for i := 0; i < MaxLog+3; i++ {
		_, err := s.Report(ctx, access.Agent("bee"), tk.ID, Report{Log: "line"})
		require.NoError(t, err)
	}
	e, err := s.Get(ctx, access.Operator, tk.ID)
	require.NoError(t, err)
	assert.Len(t, e.Task.Log, MaxLog)
	assert.Equal(t, 3, e.Task.Dropped)
}

func TestTheOwnerOrTheOperatorCancels(t *testing.T) {
	g := reportGrants()
	s, fg := service(t, g)
	ctx := context.Background()
	mine, err := s.Create(ctx, access.Agent("bee"), newTask("r"))
	require.NoError(t, err)
	fg.asked = nil

	_, err = s.Cancel(ctx, access.Agent("wasp"), mine.ID, "")
	assert.ErrorIs(t, err, ErrDenied, "another agent that sees it may not")
	assert.Empty(t, fg.asked)

	got, err := s.Cancel(ctx, access.Agent("bee"), mine.ID, "not needed")
	require.NoError(t, err)
	assert.Equal(t, Canceled, got.Status)
	assert.Equal(t, LogEntry{At: at, By: "agent:bee", Kind: EntryStatus, Status: Canceled, Text: "not needed"}, got.Log[len(got.Log)-1])
	require.Len(t, fg.asked, 1)
	assert.Equal(t, ActionCancel, fg.asked[0].Tool)
	e, err := s.Get(ctx, access.Operator, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, FolderArchive, e.Folder)
	_, err = s.Cancel(ctx, access.Agent("bee"), mine.ID, "")
	assert.ErrorIs(t, err, ErrBadRequest, "already over")

	theirs, err := s.Create(ctx, access.Agent("bee"), newTask("r"))
	require.NoError(t, err)
	_, err = s.Cancel(ctx, access.Operator, theirs.ID, "")
	assert.NoError(t, err, "the operator may cancel a task it can see")

	g["operator"] = nil
	third, err := s.Create(ctx, access.Agent("bee"), newTask("r"))
	require.NoError(t, err)
	_, err = s.Cancel(ctx, access.Operator, third.ID, "")
	assert.ErrorIs(t, err, ErrNotFound, "but not one it cannot see")
}

func TestAGateRefusedCancelLeavesTheTask(t *testing.T) {
	s, fg := service(t, reportGrants())
	ctx := context.Background()
	mine, err := s.Create(ctx, access.Agent("bee"), newTask("r"))
	require.NoError(t, err)
	fg.deny = true
	_, err = s.Cancel(ctx, access.Agent("bee"), mine.ID, "")
	assert.ErrorIs(t, err, ErrDenied)
	e, err := s.Get(ctx, access.Operator, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, Open, e.Task.Status)
	assert.Equal(t, FolderOpen, e.Folder)
}

func TestALogEntryIsValidated(t *testing.T) {
	base := Task{ID: "t-20261121-183000-abcdef", Title: "t", Description: "d", Done: "x", Tags: []string{"r"}, Owner: "operator", Status: Open}
	for name, e := range map[string]LogEntry{
		"unknown kind":            {By: "operator", Kind: "note"},
		"status entry, no status": {By: "operator", Kind: EntryStatus},
		"comment with a status":   {By: "operator", Kind: EntryComment, Status: Done},
		"no by":                   {Kind: EntryComment},
		"long text":               {By: "operator", Kind: EntryLog, Text: strings.Repeat("x", MaxEntry+1)},
	} {
		tk := base
		tk.Log = []LogEntry{e}
		assert.Error(t, tk.Validate(), name)
	}
}

func TestABrokenFileIsNotFoundToAnAgentReportingOrCanceling(t *testing.T) {
	s, _ := service(t, reportGrants())
	id := "t-20261121-000000-000000"
	require.NoError(t, os.MkdirAll(filepath.Join(s.Store.Root, FolderOpen), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.Store.Root, FolderOpen, id+".yaml"), []byte("id: [\n"), 0o600))
	ctx := context.Background()
	_, err := s.Report(ctx, access.Agent("bee"), id, Report{Comment: "x"})
	assert.Equal(t, ErrNotFound, err)
	_, err = s.Cancel(ctx, access.Agent("bee"), id, "")
	assert.Equal(t, ErrNotFound, err)
	_, err = s.Cancel(ctx, access.Operator, id, "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound, "the operator is told the file is broken")
}
