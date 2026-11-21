package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

type fakeOneOffs struct {
	mu       sync.Mutex
	recipes  []Recipe
	spawned  []string // "owner recipe -> name"
	killed   []string // "owner name"
	spawnErr error
	killErr  error
	gone     bool
	block    chan struct{}
	n        int
}

func (f *fakeOneOffs) Recipes() []Recipe { return f.recipes }
func (f *fakeOneOffs) Spawn(_ context.Context, owner access.Subject, recipe, secret string) (string, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	f.n++
	name := recipe + "-" + string(rune('a'+f.n-1))
	entry := owner.String() + " " + recipe + " -> " + name
	if secret != "" {
		entry += " with " + secret
	}
	f.spawned = append(f.spawned, entry)
	return name, nil
}
func (f *fakeOneOffs) Kill(_ context.Context, owner access.Subject, agent string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.killErr != nil {
		return "", false, f.killErr
	}
	f.killed = append(f.killed, owner.String()+" "+agent)
	return "h-1", f.gone, nil
}
func (f *fakeOneOffs) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spawned...), append([]string(nil), f.killed...)
}

func oneOffDispatcher(t *testing.T, g grants, fleet *fakeFleet, h Headroom, o *fakeOneOffs) (*Dispatcher, *Service) {
	t.Helper()
	d, s := dispatcher(t, g, fleet, h)
	d.OneOffs = o
	return d, s
}

// passUntil runs passes until cond holds: a spawn or a kill runs beside the pass.
func passUntil(t *testing.T, d *Dispatcher, cond func() bool) {
	t.Helper()
	require.Eventually(t, func() bool { d.Pass(context.Background()); return cond() }, 5*time.Second, 10*time.Millisecond)
}

func TestWithNoAgentFreeAOneOffIsSpawnedForTheOwnerAndAssigned(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r", "temp"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })

	got := get(t, s, tk.ID)
	assert.Equal(t, "writer-a", got.Assignee)
	assert.True(t, got.OneOff)
	assert.Equal(t, Assigned, got.Status)
	assert.Equal(t, "assigned to writer-a, a one-off from recipe writer", got.Log[len(got.Log)-1].Text)
	spawned, _ := o.snapshot()
	assert.Equal(t, []string{"operator writer -> writer-a"}, spawned, "spawned with the owner as requester")
	require.Len(t, fleet.delivered["writer-a"], 1)
	assert.Contains(t, fleet.delivered["writer-a"][0], "You are assigned task "+tk.ID)
}

func TestAnExistingAgentIsPreferredToAOneOff(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	assert.Equal(t, "bee", get(t, s, tk.ID).Assignee)
	spawned, _ := o.snapshot()
	assert.Empty(t, spawned)
}

func TestARecipeMustCarryEveryTagAndHaveHeadroom(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "half", Tags: []string{"r"}, Account: "sub"}, {Name: "broke", Tags: []string{"r", "x"}, Account: "spent"}}}
	d, s := oneOffDispatcher(t, opGrants("r", "x"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r", "x")
	d.Pass(context.Background())
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "; no recipe that carries every tag has budget headroom")

	o.recipes = o.recipes[:1]
	d.Pass(context.Background())
	got = get(t, s, tk.ID)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "; no recipe carries every tag")
	spawned, _ := o.snapshot()
	assert.Empty(t, spawned)
}

func TestAFailedSpawnIsNotedAndTheTaskStaysOpen(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, spawnErr: errors.New("request denied: operator said no")}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	passUntil(t, d, func() bool {
		l := get(t, s, tk.ID).Log
		return len(l) > 0 && l[len(l)-1].By == Pao
	})
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Equal(t, "waiting: a one-off from recipe writer could not be started: request denied: operator said no", got.Log[len(got.Log)-1].Text)
}

func TestOneSpawnAtATimePerTask(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, block: make(chan struct{})}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	for range 5 {
		d.Pass(context.Background())
	}
	close(o.block)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	spawned, _ := o.snapshot()
	assert.Len(t, spawned, 1, "passes while a spawn is under way start no other")
}

func TestAOneOffWhoseTaskWentElsewhereIsKilled(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, block: make(chan struct{})}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	_, err := s.Cancel(context.Background(), access.Operator, tk.ID, "")
	require.NoError(t, err)
	close(o.block)
	require.Eventually(t, func() bool { _, k := o.snapshot(); return len(k) == 1 }, 5*time.Second, 10*time.Millisecond)
	_, killed := o.snapshot()
	assert.Equal(t, []string{"operator writer-a"}, killed)
	assert.Empty(t, get(t, s, tk.ID).Assignee)
}

func TestAOneOffThatCannotBeToldIsKilledAndTheTaskOpenAgain(t *testing.T) {
	fleet := &fakeFleet{fail: map[string]error{"writer-a": errors.New("not ready")}}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	require.Eventually(t, func() bool { _, k := o.snapshot(); return len(k) == 1 }, 5*time.Second, 10*time.Millisecond)
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Empty(t, got.Assignee)
	assert.False(t, got.OneOff == true && got.Assignee == "writer-a")
}

func TestAOneOffIsKilledWhenItsTaskIsOverAndTheHarvestNoted(t *testing.T) {
	for _, end := range []string{"done", "canceled"} {
		fleet := &fakeFleet{}
		o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
		g := opGrants("r")
		g["agent:boss"] = []access.Grant{tagGrant("agent:boss", "r")}
		d, s := oneOffDispatcher(t, g, fleet, headroomOf{"sub": true}, o)
		tk := create(t, s, access.Agent("boss"), "r")
		passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
		if end == "done" {
			_, err := s.Report(context.Background(), access.Agent("writer-a"), tk.ID, Report{Status: Done})
			require.NoError(t, err)
		} else {
			_, err := s.Cancel(context.Background(), access.Agent("boss"), tk.ID, "")
			require.NoError(t, err)
		}
		passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
		_, killed := o.snapshot()
		assert.Equal(t, []string{"agent:boss writer-a"}, killed, "%s: killed with the owner as requester", end)
		got := get(t, s, tk.ID)
		assert.Equal(t, "one-off writer-a removed; its outputs are in harvest h-1", got.Log[len(got.Log)-1].Text, end)
		for range 3 {
			d.Pass(context.Background())
		}
		_, killed = o.snapshot()
		assert.Len(t, killed, 1, "%s: killed once", end)
	}
}

func TestAConfiguredAssigneeIsNeverKilled(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	o := &fakeOneOffs{}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	_, err := s.Report(context.Background(), access.Agent("bee"), tk.ID, Report{Status: Done})
	require.NoError(t, err)
	for range 3 {
		d.Pass(context.Background())
	}
	time.Sleep(50 * time.Millisecond)
	_, killed := o.snapshot()
	assert.Empty(t, killed)
}

func TestARefusedKillIsNotedAndNotRetriedAtOnce(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	o.mu.Lock()
	o.killErr = errors.New("request denied")
	o.mu.Unlock()
	_, err := s.Report(context.Background(), access.Agent("writer-a"), tk.ID, Report{Status: Failed})
	require.NoError(t, err)
	passUntil(t, d, func() bool {
		l := get(t, s, tk.ID).Log
		return l[len(l)-1].Text == "one-off writer-a could not be removed: request denied"
	})
	o.mu.Lock()
	o.killErr = nil
	o.mu.Unlock()
	for range 3 {
		d.Pass(context.Background())
	}
	time.Sleep(50 * time.Millisecond)
	assert.False(t, get(t, s, tk.ID).Removed, "not retried before the wait is over")

	s.Now = func() time.Time { return at.Add(retryRemoval + time.Second) }
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
}

func TestAOneOffAlreadyGoneIsNotedSo(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	o.gone = true
	_, err := s.Report(context.Background(), access.Agent("writer-a"), tk.ID, Report{Status: Done})
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
	got := get(t, s, tk.ID)
	assert.Equal(t, "one-off writer-a was already gone", got.Log[len(got.Log)-1].Text)
}

type quiet map[string]bool

func (q quiet) Stalled(agent string) bool { return q[agent] }

func TestAQuietAssigneeIsNotedOnceAndAgainWhenActive(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	q := quiet{}
	d.Liveness = q
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	d.Pass(context.Background())
	notes := func() []string {
		var out []string
		for _, e := range get(t, s, tk.ID).Log {
			if e.By == Pao && e.Kind == EntryLog {
				out = append(out, e.Text)
			}
		}
		return out
	}
	assert.Empty(t, notes(), "an active assignee is not noted")
	q["bee"] = true
	for range 3 {
		d.Pass(context.Background())
	}
	assert.Equal(t, []string{"assignee bee has gone quiet"}, notes())
	q["bee"] = false
	d.Pass(context.Background())
	d.Pass(context.Background())
	assert.Equal(t, []string{"assignee bee has gone quiet", "assignee bee is active again"}, notes())
}

func TestACanceledTasksAssigneeIsToldToStop(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	s.Tell = fleet.Deliver
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	_, err := s.Cancel(context.Background(), access.Operator, tk.ID, "not needed")
	require.NoError(t, err)
	require.Len(t, fleet.delivered["bee"], 2)
	assert.Equal(t, "Task "+tk.ID+" (Weekly report) was canceled by operator. Stop working on it. Reason: not needed", fleet.delivered["bee"][1])
}

func TestRemoveKillsOnlyAFinishedOneOff(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	running := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	require.Equal(t, "bee", get(t, s, running.ID).Assignee)
	// A configured agent's finished task, and a one-off's task still running.
	_, err := s.Report(context.Background(), access.Agent("bee"), running.ID, Report{Status: Done})
	require.NoError(t, err)
	live := create(t, s, access.Operator, "r")
	tk := get(t, s, live.ID)
	tk.Assignee, tk.Status, tk.OneOff = "writer-z", Running, true
	require.NoError(t, s.Store.Put(FolderOpen, tk))
	for _, id := range []string{running.ID, live.ID} {
		require.True(t, d.begin(d.removing, id))
		d.remove(context.Background(), id)
	}
	_, killed := o.snapshot()
	assert.Empty(t, killed)
}

func TestAQuietAssigneeIsNotNotedTwiceAcrossItsOwnReports(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	d.Liveness = quiet{"bee": true}
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	d.Pass(context.Background())
	_, err := s.Report(context.Background(), access.Agent("bee"), tk.ID, Report{Log: "still here"})
	require.NoError(t, err)
	d.Pass(context.Background())
	n := 0
	for _, e := range get(t, s, tk.ID).Log {
		if e.Text == "assignee bee has gone quiet" {
			n++
		}
	}
	assert.Equal(t, 1, n)
}

func TestWhileAOneOffStartsTheTaskWaitsForIt(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, block: make(chan struct{})}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	d.Pass(context.Background())
	// An agent frees up while the one-off is starting: the task is the one-off's.
	fleet.mu.Lock()
	fleet.candidates = []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}
	fleet.mu.Unlock()
	d.Pass(context.Background())
	assert.Empty(t, get(t, s, tk.ID).Assignee)
	close(o.block)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	assert.Equal(t, "writer-a", get(t, s, tk.ID).Assignee)
}

func TestAFailedSpawnIsNotTriedAgainBeforeTheWaitIsOver(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, spawnErr: errors.New("request denied")}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	var attempts int
	o2 := &countingOneOffs{fakeOneOffs: o}
	d.OneOffs = o2
	passUntil(t, d, func() bool { l := get(t, s, tk.ID).Log; return len(l) > 0 && l[len(l)-1].By == Pao })
	for range 5 {
		d.Pass(context.Background())
	}
	time.Sleep(50 * time.Millisecond)
	attempts = o2.count()
	assert.Equal(t, 1, attempts, "a refused spawn is not asked again on every pass")

	o.mu.Lock()
	o.spawnErr = nil
	o.mu.Unlock()
	s.Now = func() time.Time { return at.Add(retrySpawn + time.Second) }
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	assert.Equal(t, 2, o2.count())
}

type countingOneOffs struct {
	*fakeOneOffs
	mu     sync.Mutex
	spawns int
}

func (c *countingOneOffs) Spawn(ctx context.Context, owner access.Subject, recipe, secret string) (string, error) {
	c.mu.Lock()
	c.spawns++
	c.mu.Unlock()
	return c.fakeOneOffs.Spawn(ctx, owner, recipe, secret)
}

func (c *countingOneOffs) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.spawns }
