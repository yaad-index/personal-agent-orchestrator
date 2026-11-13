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
