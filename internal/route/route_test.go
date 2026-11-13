package route

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

type fakeLauncher struct {
	launches atomic.Int32
	failFor  string
	// delay keeps a launch in flight long enough for racing inputs to overlap.
	delay time.Duration
}

func (f *fakeLauncher) Launch(_ context.Context, p core.Profile) error {
	f.launches.Add(1)
	time.Sleep(f.delay)
	if p.Name == f.failFor {
		return errors.New("boom")
	}
	return nil
}

type fakeDeliverer struct {
	mu   sync.Mutex
	got  map[string][]core.Message
	fail string
}

func (f *fakeDeliverer) Deliver(_ context.Context, p core.Profile, m core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Name == f.fail {
		return errors.New("paste failed")
	}
	if f.got == nil {
		f.got = make(map[string][]core.Message)
	}
	f.got[p.Name] = append(f.got[p.Name], m)
	return nil
}

func newRouter(t *testing.T, profiles ...core.Profile) (*Router, *registry.Registry, *fakeLauncher, *fakeDeliverer) {
	t.Helper()
	reg := registry.New()
	for _, p := range profiles {
		require.NoError(t, reg.Add(p))
	}
	l, d := &fakeLauncher{}, &fakeDeliverer{}
	return New(reg, l, d), reg, l, d
}

var msg = core.Message{Sender: "h", Text: "hi"}

func TestRouteByNameLaunchesStoppedAgent(t *testing.T) {
	r, reg, l, d := newRouter(t, core.Profile{Name: "a"})

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "a"}, msg)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.True(t, res[0].Launched)
	assert.NoError(t, res[0].Err)
	assert.EqualValues(t, 1, l.launches.Load())
	assert.Equal(t, []core.Message{msg}, d.got["a"])

	_, st, _ := reg.Lookup("a")
	assert.Equal(t, core.Running, st)
}

func TestRouteToRunningAgentDoesNotLaunch(t *testing.T) {
	r, reg, l, d := newRouter(t, core.Profile{Name: "a"})
	require.NoError(t, reg.SetState("a", core.Running))

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "a"}, msg)
	require.NoError(t, err)
	assert.False(t, res[0].Launched)
	assert.EqualValues(t, 0, l.launches.Load())
	assert.Len(t, d.got["a"], 1)
}

func TestRouteByTagFansOut(t *testing.T) {
	r, _, _, d := newRouter(t,
		core.Profile{Name: "a", Tags: []string{"x"}},
		core.Profile{Name: "b", Tags: []string{"x"}},
		core.Profile{Name: "c", Tags: []string{"y"}},
	)

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByTag, Value: "x"}, msg)
	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, "a", res[0].Agent)
	assert.Equal(t, "b", res[1].Agent)
	assert.Len(t, d.got["a"], 1)
	assert.Len(t, d.got["b"], 1)
	assert.Empty(t, d.got["c"])
}

func TestRouteFanOutIsolatesFailures(t *testing.T) {
	r, reg, l, d := newRouter(t,
		core.Profile{Name: "a", Tags: []string{"x"}},
		core.Profile{Name: "b", Tags: []string{"x"}},
	)
	l.failFor = "a"

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByTag, Value: "x"}, msg)
	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Error(t, res[0].Err)
	assert.NoError(t, res[1].Err)
	assert.Len(t, d.got["b"], 1)

	_, st, _ := reg.Lookup("a")
	assert.Equal(t, core.Stopped, st, "a failed launch leaves the agent stopped")
}

func TestRouteDeliverFailureIsReported(t *testing.T) {
	r, _, _, d := newRouter(t, core.Profile{Name: "a"})
	d.fail = "a"

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "a"}, msg)
	require.NoError(t, err)
	assert.Error(t, res[0].Err)
}

func TestRouteUnresolvableTarget(t *testing.T) {
	r, _, _, _ := newRouter(t, core.Profile{Name: "a"})

	_, err := r.Route(context.Background(), core.Target{}, msg)
	assert.ErrorIs(t, err, core.ErrNoTarget)

	_, err = r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "nope"}, msg)
	assert.ErrorIs(t, err, core.ErrNoMatch)

	_, err = r.Route(context.Background(), core.Target{Kind: core.ByTag, Value: "nope"}, msg)
	assert.ErrorIs(t, err, core.ErrNoMatch)
}

func TestConcurrentInputsLaunchOnce(t *testing.T) {
	r, _, l, d := newRouter(t, core.Profile{Name: "a"})
	l.delay = 20 * time.Millisecond

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "a"}, msg)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, l.launches.Load())
	assert.Len(t, d.got["a"], 20)
}

func tempRouter(t *testing.T, now time.Time) (*Router, *registry.Registry, *fakeDeliverer) {
	t.Helper()
	reg := registry.New()
	require.NoError(t, reg.AddTemp(core.Profile{Name: "t"}, registry.TempInfo{Created: now.Add(-time.Hour)}))
	d := &fakeDeliverer{fail: "none"}
	return New(reg, &fakeLauncher{}, d, WithClock(func() time.Time { return now })), reg, d
}

func TestDeliveryMarksTheTempAgentUsed(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, reg, _ := tempRouter(t, now)

	_, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "t"}, msg)
	require.NoError(t, err)

	v := reg.Temps()[0]
	assert.True(t, v.Received)
	assert.Equal(t, now, v.LastActivity)
}

func TestFailedDeliveryIsNotUse(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, reg, d := tempRouter(t, now)
	d.fail = "t"

	res, err := r.Route(context.Background(), core.Target{Kind: core.ByName, Value: "t"}, msg)
	require.NoError(t, err)
	require.Error(t, res[0].Err)

	assert.False(t, reg.Temps()[0].Received, "a message that never reached the agent does not make it used")
}

func TestEnsureLaunchesOnceAndReportsIt(t *testing.T) {
	r, reg, l, _ := newRouter(t, core.Profile{Name: "a"})
	p, _, _ := reg.Lookup("a")

	launched, err := r.Ensure(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, launched)
	launched, err = r.Ensure(context.Background(), p)
	require.NoError(t, err)
	assert.False(t, launched, "an agent already running is not launched again")
	assert.EqualValues(t, 1, l.launches.Load())
	_, st, _ := reg.Lookup("a")
	assert.Equal(t, core.Running, st)
}

func TestEnsureFailureLeavesTheAgentStopped(t *testing.T) {
	r, reg, l, _ := newRouter(t, core.Profile{Name: "a"})
	l.failFor = "a"
	p, _, _ := reg.Lookup("a")
	_, err := r.Ensure(context.Background(), p)
	require.Error(t, err)
	_, st, _ := reg.Lookup("a")
	assert.Equal(t, core.Stopped, st)
}

func TestNoMatchSaysTheSameThingTheRouterDoes(t *testing.T) {
	r, _, _, _ := newRouter(t)
	_, err := r.Resolve(core.Target{Kind: core.ByName, Value: "ghost"})
	assert.Equal(t, NoMatch(core.Target{Kind: core.ByName, Value: "ghost"}).Error(), err.Error())
	_, err = r.Resolve(core.Target{Kind: core.ByTag, Value: "ghosts"})
	assert.Equal(t, NoMatch(core.Target{Kind: core.ByTag, Value: "ghosts"}).Error(), err.Error())
	assert.ErrorIs(t, NoMatch(core.Target{Kind: core.ByTag, Value: "x"}), core.ErrNoMatch)
}

func TestDeliverReachesExactlyTheGivenAgentsAndIsolatesFailures(t *testing.T) {
	r, reg, l, d := newRouter(t, core.Profile{Name: "a"}, core.Profile{Name: "b"}, core.Profile{Name: "c"})
	l.failFor = "a"
	pa, _, _ := reg.Lookup("a")
	pb, _, _ := reg.Lookup("b")

	res := r.Deliver(context.Background(), []core.Profile{pa, pb}, msg)
	require.Len(t, res, 2)
	assert.Error(t, res[0].Err)
	assert.NoError(t, res[1].Err)
	assert.Len(t, d.got["b"], 1)
	assert.Empty(t, d.got["c"], "an agent not given is not delivered to")
}
