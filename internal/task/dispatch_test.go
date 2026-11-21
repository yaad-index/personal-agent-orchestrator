package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type fakeFleet struct {
	mu         sync.Mutex
	candidates []Candidate
	delivered  map[string][]string
	fail       map[string]error
}

// sent is what agent has been delivered, read under the fleet's lock: a
// one-off is told of its task beside the pass.
func (f *fakeFleet) sent(agent string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.delivered[agent]...)
}

func (f *fakeFleet) Candidates() []Candidate { f.mu.Lock(); defer f.mu.Unlock(); return f.candidates }
func (f *fakeFleet) Deliver(_ context.Context, agent, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[agent]; err != nil {
		return err
	}
	if f.delivered == nil {
		f.delivered = map[string][]string{}
	}
	f.delivered[agent] = append(f.delivered[agent], text)
	return nil
}

// headroomOf gives headroom to the accounts listed, judged by a floor the
// rule must stay under: a rule with a floor above 50 sees none.
type headroomOf map[string]bool

func (h headroomOf) Headroom(account string, rule core.BudgetRule) bool {
	return h[account] && rule.Floor <= 50
}

func dispatcher(t *testing.T, g grants, fleet *fakeFleet, h Headroom) (*Dispatcher, *Service) {
	t.Helper()
	s, _ := service(t, g)
	return NewDispatcher(s, fleet, h), s
}

func create(t *testing.T, s *Service, who access.Subject, tags ...string) Task {
	t.Helper()
	tk, err := s.Create(context.Background(), who, newTask(tags...))
	require.NoError(t, err)
	return tk
}

func get(t *testing.T, s *Service, id string) Task {
	t.Helper()
	e, err := s.Get(context.Background(), access.Operator, id)
	require.NoError(t, err)
	return e.Task
}

func opGrants(tags ...string) grants {
	var gs []access.Grant
	for _, tag := range tags {
		gs = append(gs, tagGrant("operator", tag))
	}
	return grants{"operator": gs}
}

func TestAnOpenTaskGoesToAFreeAgentCarryingItsTagsWithHeadroom(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r", "x"}, Account: "sub", Running: true}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())

	got := get(t, s, tk.ID)
	assert.Equal(t, Assigned, got.Status)
	assert.Equal(t, "bee", got.Assignee)
	assert.Equal(t, LogEntry{At: at, By: Pao, Kind: EntryStatus, Status: Assigned, Text: "assigned to bee"}, got.Log[len(got.Log)-1])
	require.Len(t, fleet.sent("bee"), 1)
	brief := fleet.sent("bee")[0]
	assert.Contains(t, brief, "You are assigned task "+tk.ID+": Weekly report")
	assert.Contains(t, brief, "Done when: The report is posted.")
	assert.Contains(t, brief, "report_task tool, id "+tk.ID)

	d.Pass(context.Background())
	assert.Len(t, fleet.sent("bee"), 1, "an assigned task is not dispatched again")
}

func TestAnAgentMustCarryEveryTagAndBeReachableByTheOwner(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{
		{Name: "half", Tags: []string{"r"}, Account: "sub", Running: true},
		{Name: "far", Tags: []string{"r", "x"}, Account: "sub"},
	}}
	// The owner reaches tags r and x, but "far" carries them and is still
	// reached by tag; take its reach away by name-only grants.
	g := grants{"operator": {tagGrant("operator", "r"), tagGrant("operator", "x")}, "agent:boss": {
		tagGrant("agent:boss", "r"), tagGrant("agent:boss", "x"),
	}}
	d, s := dispatcher(t, g, fleet, headroomOf{"sub": true})
	both := create(t, s, access.Operator, "r", "x")
	d.Pass(context.Background())
	assert.Equal(t, "far", get(t, s, both.ID).Assignee, "the agent carrying only r is not given a task tagged r and x, though it runs and would be preferred")

	// An owner that may not reach the only carrier.
	fleet.candidates = []Candidate{{Name: "other", Tags: []string{"r", "x"}, Account: "sub"}}
	g["agent:boss"] = []access.Grant{{Subject: access.Agent("boss"), Kind: access.ResourceAgent, Name: "far"}}
	tk, err := s.Create(context.Background(), access.Operator, newTask("r"))
	require.NoError(t, err)
	tk.Owner = "agent:boss"
	require.NoError(t, s.Store.Put(FolderOpen, tk))
	d.Pass(context.Background())
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "no agent its owner may reach, and that reaches its tools, carries every tag of r")
}

func TestAnAgentHoldsOneTaskAtATime(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	first := create(t, s, access.Operator, "r")
	s.Now = func() time.Time { return at.Add(time.Minute) }
	second := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	assert.Equal(t, "bee", get(t, s, first.ID).Assignee, "the oldest goes first")
	waiting := get(t, s, second.ID)
	assert.Equal(t, Open, waiting.Status)
	assert.Equal(t, "waiting: every agent that could run it holds another task", waiting.Log[len(waiting.Log)-1].Text)

	// bee finishes: the next pass gives it the second.
	_, err := s.Report(context.Background(), access.Agent("bee"), first.ID, Report{Status: Done})
	require.NoError(t, err)
	d.Pass(context.Background())
	assert.Equal(t, "bee", get(t, s, second.ID).Assignee)
}

func TestATaskWaitsForHeadroomAndSaysSoOncePerReason(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	h := headroomOf{"sub": false}
	d, s := dispatcher(t, opGrants("r"), fleet, h)
	tk := create(t, s, access.Operator, "r")
	for range 3 {
		d.Pass(context.Background())
	}
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	var waits []string
	for _, e := range got.Log {
		if e.By == Pao {
			waits = append(waits, e.Text)
		}
	}
	assert.Equal(t, []string{"waiting: no agent that could run it has budget headroom"}, waits, "said once, not every pass")

	h["sub"] = true
	d.Pass(context.Background())
	assert.Equal(t, "bee", get(t, s, tk.ID).Assignee)
}

func TestHeadroomIsJudgedByEachAgentsOwnRule(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{
		{Name: "strict", Tags: []string{"r"}, Account: "sub", Budget: core.BudgetRule{Floor: 90}, Running: true},
		{Name: "lax", Tags: []string{"r"}, Account: "sub"},
	}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	assert.Equal(t, "lax", get(t, s, tk.ID).Assignee, "the running agent's own rule sees no headroom")
}

func TestARunningAgentIsPreferred(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{
		{Name: "a-stopped", Tags: []string{"r"}, Account: "sub"},
		{Name: "z-running", Tags: []string{"r"}, Account: "sub", Running: true},
	}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	assert.Equal(t, "z-running", get(t, s, tk.ID).Assignee)
}

func TestAFailedDeliveryTakesTheAssignmentBack(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}, fail: map[string]error{"bee": errors.New("not ready")}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Empty(t, got.Assignee)
	last := got.Log[len(got.Log)-1]
	assert.Equal(t, Open, last.Status)
	assert.Equal(t, "bee could not be told of it: not ready", last.Text)

	fleet.mu.Lock()
	fleet.fail = nil
	fleet.mu.Unlock()
	d.Pass(context.Background())
	assert.Equal(t, "bee", get(t, s, tk.ID).Assignee, "the next pass tries again")
}

func TestNoCandidateHasNoHeadroomInterfaceToAsk(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}}}}
	d, s := dispatcher(t, opGrants("r"), fleet, nil)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	assert.Equal(t, Open, get(t, s, tk.ID).Status, "with no budget to ask, nothing has headroom")
}

func TestAKickWakesRun(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	g := opGrants("r", "nobody-has-it")
	d, s := dispatcher(t, g, fleet, headroomOf{"sub": true})
	s.Changed = d.Kick
	// A task no agent can run marks when Run's first pass is over.
	marker := create(t, s, access.Operator, "nobody-has-it")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx, time.Hour); close(done) }()
	require.Eventually(t, func() bool { return len(get(t, s, marker.ID).Log) > 0 }, 5*time.Second, 10*time.Millisecond)

	tk := create(t, s, access.Operator, "r")
	require.Eventually(t, func() bool { return get(t, s, tk.ID).Assignee == "bee" }, 5*time.Second, 10*time.Millisecond, "a create kicks a pass long before the hour")
	cancel()
	<-done
}

func TestATaskTakenMeanwhileIsNotAssignedAgain(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	tk := create(t, s, access.Operator, "r")
	// Between a pass listing the task and assigning it, another took it.
	taken := get(t, s, tk.ID)
	taken.Assignee, taken.Status = "wasp", Assigned
	require.NoError(t, s.Store.Put(FolderOpen, taken))
	assert.False(t, d.assign(context.Background(), tk.ID, "bee"))
	assert.Equal(t, "wasp", get(t, s, tk.ID).Assignee)
	assert.Empty(t, fleet.sent("bee"))
}

func TestTheBriefNamesTheSecretButNeverItsValue(t *testing.T) {
	tk := Task{ID: "t-20261121-183000-abcdef", Title: "T", Description: "D", Done: "X", How: "skill: s", Parent: "t-20261121-000000-000000",
		Attachments: Attachments{Tools: []string{"Read"}, Scripts: []string{"run.sh"}, Secret: "report-api"}}
	b := Brief(tk)
	for _, want := range []string{"How: skill: s", "Tools: Read", "Scripts: run.sh", "Secret: report-api. Its value is in the file whose path is in $PAO_SECRET_REPORT_API, for this run only. Read it with a file-reading tool. Never print, log, commit or copy the value anywhere.", "Created from task: t-20261121-000000-000000"} {
		assert.Contains(t, b, want)
	}
	assert.False(t, strings.Contains(Brief(Task{ID: tk.ID}), "Secret:"), "no secret, no line")
	assert.Contains(t, BriefWith(tk, "/run/secrets/a/report-api"), "Its value is in the file /run/secrets/a/report-api (its path is also in $PAO_SECRET_REPORT_API)", "a shell-less agent is told the file itself")
}
