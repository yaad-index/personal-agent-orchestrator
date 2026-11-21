package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

type grants map[string][]access.Grant

func (g grants) Of(s access.Subject) []access.Grant { return g[s.String()] }

func tagGrant(subject, tag string) access.Grant {
	s, _ := access.ParseSubject(subject)
	return access.Grant{Subject: s, Kind: access.ResourceAgent, Tag: tag}
}

type fakeGate struct {
	asked []gate.Request
	deny  bool
}

func (g *fakeGate) Decide(_ context.Context, r gate.Request) gate.Decision {
	g.asked = append(g.asked, r)
	if g.deny {
		return gate.Decision{Allow: false, Reason: "operator said no"}
	}
	return gate.Decision{Allow: true}
}

var at = time.Date(2026, 11, 21, 18, 30, 0, 0, time.UTC)

func service(t *testing.T, g grants) (*Service, *fakeGate) {
	t.Helper()
	fg := &fakeGate{}
	return &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "tasks")}, Auth: access.Matrix{Grants: g}, Gate: fg, Now: func() time.Time { return at }}, fg
}

func newTask(tags ...string) New {
	return New{Title: "Weekly report", Description: "Write the weekly report.", Done: "The report is posted.", Tags: tags}
}

func TestATaskIsCreatedOpenOwnedByItsCreatorAndKeptAsAFile(t *testing.T) {
	s, fg := service(t, grants{"agent:bee": {tagGrant("agent:bee", "reports")}})
	n := newTask("reports")
	n.How = "skill: weekly-report"
	n.Attachments = Attachments{Tools: []string{"Read"}, Scripts: []string{"scripts/report.sh"}, Secret: "report-api"}
	got, err := s.Create(context.Background(), access.Agent("bee"), n)
	require.NoError(t, err)
	assert.True(t, ValidID(got.ID), got.ID)
	assert.True(t, strings.HasPrefix(got.ID, "t-20261121-183000-"))
	assert.Equal(t, Open, got.Status)
	assert.Equal(t, "agent:bee", got.Owner)
	assert.Equal(t, at, got.Created)
	assert.Empty(t, got.Assignee)

	require.Len(t, fg.asked, 1, "an agent's create is put to the gate")
	assert.Equal(t, ActionCreate, fg.asked[0].Tool)
	assert.Equal(t, "bee", fg.asked[0].Agent)
	var detail map[string]string
	require.NoError(t, json.Unmarshal(fg.asked[0].Input, &detail))
	assert.Equal(t, map[string]string{"title": "Weekly report", "tags": "reports"}, detail)

	path := filepath.Join(s.Store.Root, FolderOpen, got.ID+".yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "secret: report-api", "the task file holds the secret's name")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	e, err := s.Get(context.Background(), access.Agent("bee"), got.ID)
	require.NoError(t, err)
	assert.Equal(t, got, e.Task, "what is read back is what was created")
	assert.Equal(t, FolderOpen, e.Folder)
}

func TestATaskNeedsTagsItsCreatorMaySendWorkTo(t *testing.T) {
	s, fg := service(t, grants{"agent:bee": {tagGrant("agent:bee", "reports")}})
	ctx := context.Background()
	_, err := s.Create(ctx, access.Agent("bee"), newTask("reports", "deploy"))
	assert.ErrorIs(t, err, ErrNotFound, "one tag out of reach refuses the task, as not found")
	_, err = s.Create(ctx, access.Agent("wasp"), newTask("reports"))
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Create(ctx, access.Operator, newTask("reports"))
	assert.ErrorIs(t, err, ErrNotFound, "the operator too is held to its grants")
	assert.Empty(t, fg.asked, "nothing out of reach reaches the gate")
	all, err := s.Store.List()
	require.NoError(t, err)
	assert.Empty(t, all, "and nothing is written")
}

func TestTheGateRefusingACreateWritesNothing(t *testing.T) {
	s, fg := service(t, grants{"agent:bee": {tagGrant("agent:bee", "reports")}})
	fg.deny = true
	_, err := s.Create(context.Background(), access.Agent("bee"), newTask("reports"))
	require.ErrorIs(t, err, ErrDenied)
	assert.Contains(t, err.Error(), "operator said no")
	all, err := s.Store.List()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestTheOperatorIsNotPutToTheGate(t *testing.T) {
	s, fg := service(t, grants{"operator": {tagGrant("operator", "reports")}})
	_, err := s.Create(context.Background(), access.Operator, newTask("reports"))
	require.NoError(t, err)
	assert.Empty(t, fg.asked)
}

func TestAMalformedTaskIsABadRequest(t *testing.T) {
	s, _ := service(t, grants{"operator": {tagGrant("operator", "r")}})
	long := strings.Repeat("x", MaxText+1)
	for name, c := range map[string]struct {
		mod  func(*New)
		want string
	}{
		"no title":          {func(n *New) { n.Title = " " }, "title"},
		"two-line title":    {func(n *New) { n.Title = "a\nb" }, "title"},
		"no description":    {func(n *New) { n.Description = "" }, "description"},
		"long description":  {func(n *New) { n.Description = long }, "description"},
		"no done":           {func(n *New) { n.Done = "" }, "definition of done"},
		"long how":          {func(n *New) { n.How = long }, "how"},
		"no tags":           {func(n *New) { n.Tags = nil }, "at least one tag"},
		"bad tag":           {func(n *New) { n.Tags = []string{"R s"} }, `tag "R s"`},
		"tag twice":         {func(n *New) { n.Tags = []string{"r", "r"} }, "given twice"},
		"bad parent":        {func(n *New) { n.Parent = "../../etc/passwd" }, "parent"},
		"secret not a name": {func(n *New) { n.Attachments.Secret = "Has Space" }, "secret"},
		"empty tool":        {func(n *New) { n.Attachments.Tools = []string{""} }, "tool"},
		"script newline":    {func(n *New) { n.Attachments.Scripts = []string{"a\nb"} }, "script"},
	} {
		n := newTask("r")
		c.mod(&n)
		_, err := s.Create(context.Background(), access.Operator, n)
		require.ErrorIs(t, err, ErrBadRequest, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestATaskIsSeenByItsOwnerAndByWhoeverReachesItsTags(t *testing.T) {
	g := grants{
		"agent:bee":  {tagGrant("agent:bee", "reports")},
		"agent:wasp": {tagGrant("agent:wasp", "reports")},
		"agent:ant":  {tagGrant("agent:ant", "other")},
	}
	s, _ := service(t, g)
	ctx := context.Background()
	mine, err := s.Create(ctx, access.Agent("bee"), newTask("reports"))
	require.NoError(t, err)

	for who, want := range map[string]bool{"bee": true, "wasp": true, "ant": false} {
		_, err := s.Get(ctx, access.Agent(who), mine.ID)
		if want {
			assert.NoError(t, err, who)
		} else {
			assert.ErrorIs(t, err, ErrNotFound, who)
		}
		list, err := s.List(ctx, access.Agent(who))
		require.NoError(t, err)
		assert.Equal(t, want, len(list) == 1, who)
	}

	// The owner keeps seeing it after losing the tag.
	g["agent:bee"] = nil
	_, err = s.Get(ctx, access.Agent("bee"), mine.ID)
	assert.NoError(t, err)
}

func TestAnUnknownOrMalformedIDIsNotFound(t *testing.T) {
	s, _ := service(t, grants{"operator": {tagGrant("operator", "r")}})
	for _, id := range []string{"t-20261121-183000-abcdef", "../open/x", "", "t-1"} {
		_, err := s.Get(context.Background(), access.Operator, id)
		assert.ErrorIs(t, err, ErrNotFound, id)
	}
}

func TestAParentMustBeATaskTheCreatorMaySee(t *testing.T) {
	g := grants{"agent:bee": {tagGrant("agent:bee", "reports")}, "agent:ant": {tagGrant("agent:ant", "review")}, "agent:wasp": {tagGrant("agent:wasp", "reports"), tagGrant("agent:wasp", "review")}}
	s, _ := service(t, g)
	ctx := context.Background()
	parent, err := s.Create(ctx, access.Agent("bee"), newTask("reports"))
	require.NoError(t, err)

	child := newTask("review")
	child.Parent = parent.ID
	_, err = s.Create(ctx, access.Agent("ant"), child)
	assert.ErrorIs(t, err, ErrNotFound, "ant cannot see the parent")

	got, err := s.Create(ctx, access.Agent("wasp"), child)
	require.NoError(t, err)
	assert.Equal(t, parent.ID, got.Parent)

	child.Parent = "t-20991231-000000-000000"
	_, err = s.Create(ctx, access.Agent("wasp"), child)
	assert.ErrorIs(t, err, ErrNotFound, "a parent that does not exist")
}

func TestListAndReadArePutToTheGateForAnAgent(t *testing.T) {
	s, fg := service(t, grants{"agent:bee": {tagGrant("agent:bee", "r")}})
	ctx := context.Background()
	tk, err := s.Create(ctx, access.Agent("bee"), newTask("r"))
	require.NoError(t, err)
	fg.asked = nil
	_, err = s.List(ctx, access.Agent("bee"))
	require.NoError(t, err)
	_, err = s.Get(ctx, access.Agent("bee"), tk.ID)
	require.NoError(t, err)
	require.Len(t, fg.asked, 2)
	assert.Equal(t, ActionList, fg.asked[0].Tool)
	assert.Equal(t, ActionRead, fg.asked[1].Tool)

	fg.deny = true
	_, err = s.List(ctx, access.Agent("bee"))
	assert.ErrorIs(t, err, ErrDenied)
	_, err = s.Get(ctx, access.Agent("bee"), tk.ID)
	assert.ErrorIs(t, err, ErrDenied)
}

func TestTheStoreKeepsATaskInOneFolderAndListsNewestFirst(t *testing.T) {
	st := &Store{Root: t.TempDir()}
	mk := func(sec int) Task {
		c := at.Add(time.Duration(sec) * time.Second)
		id, err := NewID(c)
		require.NoError(t, err)
		return Task{ID: id, Title: "t", Description: "d", Done: "x", Tags: []string{"r"}, Owner: "operator", Status: Open, Created: c, Updated: c}
	}
	older, newer := mk(0), mk(5)
	require.NoError(t, st.Put(FolderOpen, older))
	require.NoError(t, st.Put(FolderOpen, newer))
	require.NoError(t, st.Put(FolderArchive, older), "moving it")
	assert.NoFileExists(t, filepath.Join(st.Root, FolderOpen, older.ID+".yaml"), "a moved task leaves its old folder")
	_, folder, err := st.Get(older.ID)
	require.NoError(t, err)
	assert.Equal(t, FolderArchive, folder)

	require.NoError(t, st.Put("waiting-budget", newer), "the folder set is open")
	all, err := st.List()
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, newer.ID, all[0].Task.ID)
	assert.Equal(t, "waiting-budget", all[0].Folder)

	for _, bad := range []string{"../x", "Open", "", "a/b"} {
		assert.Error(t, st.Put(bad, older), bad)
	}
}

func TestABrokenFileIsReportedAndTheRestStillListed(t *testing.T) {
	s, _ := service(t, grants{"operator": {tagGrant("operator", "r")}, "agent:bee": {tagGrant("agent:bee", "r")}})
	ctx := context.Background()
	good, err := s.Create(ctx, access.Operator, newTask("r"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.Store.Root, FolderOpen, "t-20261121-000000-000000.yaml"), []byte("id: t-20261121-000000-000000\nunknown: x\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(s.Store.Root, FolderOpen, "t-20261121-000001-000000.yaml"), []byte("id: t-20261121-000009-000000\n"), 0o600))

	list, err := s.List(ctx, access.Operator)
	require.Error(t, err, "the operator is told of the broken files")
	assert.Len(t, list, 1)
	assert.Equal(t, good.ID, list[0].Task.ID)

	list, err = s.List(ctx, access.Agent("bee"))
	require.NoError(t, err, "an agent is not")
	assert.Len(t, list, 1)
}

func TestAStatusIsKnownOrRefused(t *testing.T) {
	for _, s := range Statuses {
		assert.True(t, s.Valid(), s)
	}
	assert.False(t, Status("finished").Valid())
	assert.True(t, errors.Is(ErrNotFound, ErrNotFound))
}

func TestAnIDCannotReachAFileOutsideTheStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	st := &Store{Root: root}
	ok, err := NewID(at)
	require.NoError(t, err)
	require.NoError(t, st.Put(FolderOpen, Task{ID: ok, Title: "t", Description: "d", Done: "x", Tags: []string{"r"}, Owner: "operator", Status: Open, Created: at, Updated: at}))
	// A yaml file beside the store, which "<root>/open/../outside" would name.
	require.NoError(t, os.WriteFile(filepath.Join(root, "outside.yaml"), []byte("private: notes\n"), 0o600))
	_, _, err = st.Get("../outside")
	assert.ErrorIs(t, err, ErrNotFound, "never read, so never reported from")
	assert.NotContains(t, err.Error(), "private")
}

func TestAFileHoldingAnotherTasksIDIsRefused(t *testing.T) {
	st := &Store{Root: t.TempDir()}
	id, err := NewID(at)
	require.NoError(t, err)
	tk := Task{ID: id, Title: "t", Description: "d", Done: "x", Tags: []string{"r"}, Owner: "operator", Status: Open, Created: at, Updated: at}
	require.NoError(t, st.Put(FolderOpen, tk))
	other := "t-20261121-000000-000000"
	require.NoError(t, os.Rename(filepath.Join(st.Root, FolderOpen, id+".yaml"), filepath.Join(st.Root, FolderOpen, other+".yaml")))
	_, _, err = st.Get(other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds task "+id)
}
