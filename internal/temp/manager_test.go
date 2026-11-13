package temp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

const recipeYAML = `name: researcher
description: reads and reports
profile:
  backend: harness
  tags: [research]
  startup:
    command: "run-agent --name {name}"
    ready_pattern: "READY>"
    ready_timeout: 30s
  transcript:
    format: harness-jsonl
workdir:
  files:
    README.md: hello
tools: [Read]
`

// fakes ---------------------------------------------------------------

type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

type fakePlane struct {
	ev   *events
	fail error
	// atKill runs when a kill arrives, before it returns.
	atKill func(core.Profile)
}

func (p *fakePlane) Kill(_ context.Context, prof core.Profile) error {
	p.ev.add("kill:" + prof.Name)
	if p.atKill != nil {
		p.atKill(prof)
	}
	return p.fail
}

type fakeStarter struct {
	ev   *events
	fail error
	got  []core.Profile
}

func (s *fakeStarter) Ensure(_ context.Context, p core.Profile) (bool, error) {
	s.ev.add("start:" + p.Name)
	s.got = append(s.got, p)
	return s.fail == nil, s.fail
}

type fakeChannels struct {
	ev    *events
	names []string
	bound map[string]string
}

func (c *fakeChannels) HasChannel(n string) bool {
	for _, x := range c.names {
		if x == n {
			return true
		}
	}
	return false
}
func (c *fakeChannels) Channels() []string { return c.names }
func (c *fakeChannels) Bind(a, ch string) error {
	c.ev.add("bind:" + a + ":" + ch)
	c.bound[a] = ch
	return nil
}
func (c *fakeChannels) Unbind(a string) {
	c.ev.add("unbind:" + a)
	delete(c.bound, a)
}

type fakeGate struct {
	allow bool
	got   []gate.Request
}

func (g *fakeGate) Decide(_ context.Context, r gate.Request) gate.Decision {
	g.got = append(g.got, r)
	if g.allow {
		return gate.Decision{Allow: true, Via: "operator", Reason: "ok"}
	}
	return gate.Decision{Via: "operator", Reason: "no thanks"}
}

type fakeFollow struct{ ev *events }

func (f fakeFollow) Stop(n string) { f.ev.add("stop-follow:" + n) }

type reported struct {
	who  access.Subject
	text string
}

// allowAgents lets every agent past the access seam, so the gate is what
// decides.
type allowAgents struct{}

func (allowAgents) Allow(s access.Subject, _ access.Action, _ access.Resource) bool {
	return s.Kind != 0
}

type env struct {
	m        *Manager
	ev       *events
	plane    *fakePlane
	starter  *fakeStarter
	channels *fakeChannels
	gate     *fakeGate
	reg      *registry.Registry
	prov     *workdir.Provisioner
	store    *harvest.Store
	reports  *[]reported
	now      *time.Time
}

func locate(format, wd string) (string, bool) {
	if format == "harness-jsonl" {
		return filepath.Join(wd, ".t"), true
	}
	return "", false
}

func newEnv(t *testing.T, recipes map[string]string, opts ...func(*env)) *env {
	t.Helper()
	root := t.TempDir()
	rdir := filepath.Join(root, "recipes")
	require.NoError(t, os.MkdirAll(rdir, 0o700))
	for name, body := range recipes {
		require.NoError(t, os.WriteFile(filepath.Join(rdir, name+".yaml"), []byte(body), 0o600))
	}
	ev := &events{}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	e := &env{
		ev: ev, plane: &fakePlane{ev: ev}, starter: &fakeStarter{ev: ev},
		channels: &fakeChannels{ev: ev, names: []string{"local"}, bound: map[string]string{}},
		gate:     &fakeGate{}, reg: registry.New(), now: &now,
		prov: &workdir.Provisioner{Root: filepath.Join(root, "temp")},
	}
	e.store = harvest.New(filepath.Join(root, "harvest"), nil)
	var reps []reported
	e.reports = &reps
	for _, o := range opts {
		o(e)
	}
	e.m = &Manager{
		Registry: e.reg, Plane: e.plane, Starter: e.starter, Channels: e.channels,
		Workdirs: e.prov, Harvests: e.store,
		Recipes: func() (*recipe.Set, error) { return recipe.LoadDir(rdir) },
		Locate:  locate, Gate: e.gate, Follow: fakeFollow{ev},
		Surface: func(_ context.Context, who access.Subject, text string) {
			reps = append(reps, reported{who, text})
			e.reports = &reps
		},
		Now: func() time.Time { return *e.now },
	}
	return e
}

func (e *env) spawn(t *testing.T, name string) core.Profile {
	t.Helper()
	p, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: name, Requester: access.Operator})
	require.NoError(t, err)
	return p
}

func dirs(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, x := range es {
		out = append(out, x.Name())
	}
	return out
}

// Spawn ---------------------------------------------------------------

func TestSpawnCreatesARegisteredBoundLaunchedAgentInItsOwnWorkdir(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	p := e.spawn(t, "tmp-a")

	assert.Equal(t, "tmp-a", p.Name)
	assert.Equal(t, filepath.Join(e.prov.Root, "tmp-a"), p.Workdir)
	assert.FileExists(t, filepath.Join(p.Workdir, "README.md"))
	assert.Equal(t, "run-agent --name 'tmp-a'", p.Startup.Command)
	assert.Equal(t, filepath.Join(p.Workdir, ".t"), p.Transcript.Path)

	v, ok := e.reg.TempOf("tmp-a")
	require.True(t, ok, "a first-class registered agent")
	assert.Equal(t, "local", v.Info.Channel)
	assert.Equal(t, "researcher", v.Info.Recipe)
	assert.Equal(t, *e.now, v.Info.Created)
	assert.Equal(t, "local", e.channels.bound["tmp-a"])
	assert.Equal(t, []string{"bind:tmp-a:local", "start:tmp-a"}, e.ev.all(), "bound before it launches, so its first words already go to its channel")
	require.Len(t, e.starter.got, 1)
	assert.Equal(t, p, e.starter.got[0])
}

func TestSpawnGeneratesANameWhenNoneIsGiven(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	a, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Requester: access.Operator})
	require.NoError(t, err)
	b, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Requester: access.Operator})
	require.NoError(t, err)

	assert.Regexp(t, `^researcher-[0-9a-f]{6}$`, a.Name)
	assert.NotEqual(t, a.Name, b.Name)
	assert.NotEqual(t, a.Workdir, b.Workdir, "isolated from each other")
}

func TestSpawnOfAnUnknownRecipeCreatesNothing(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "nope", Requester: access.Operator})
	require.ErrorIs(t, err, ErrNoRecipe)
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())
	assert.Empty(t, e.ev.all())
}

func TestSpawnSeesARecipeInstalledAfterTheNodeStarted(t *testing.T) {
	e := newEnv(t, nil)
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Requester: access.Operator})
	require.ErrorIs(t, err, ErrNoRecipe)

	rdir := filepath.Join(filepath.Dir(e.prov.Root), "recipes")
	require.NoError(t, os.WriteFile(filepath.Join(rdir, "researcher.yaml"), []byte(recipeYAML), 0o600))
	_, err = e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Requester: access.Operator})
	require.NoError(t, err)
}

func TestSpawnChannelChoice(t *testing.T) {
	cases := []struct {
		name    string
		names   []string
		want    string
		bound   string
		wantErr bool
	}{
		{"the only channel is the default", []string{"local"}, "", "local", false},
		{"a named channel", []string{"local", "chat"}, "chat", "chat", false},
		{"several channels need a name", []string{"local", "chat"}, "", "", true},
		{"no channel at all", nil, "", "", true},
		{"an unknown channel", []string{"local"}, "nope", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) { e.channels.names = c.names })
			_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Channel: c.want, Requester: access.Operator})
			if c.wantErr {
				require.ErrorIs(t, err, ErrBadRequest)
				assert.Empty(t, dirs(t, e.prov.Root), "a refused channel provisions nothing")
				assert.Empty(t, e.reg.Temps())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.bound, e.channels.bound["a"])
		})
	}
}

func TestSpawnRefusesABadName(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	for _, name := range []string{"Bad", "a/b", "../x", "a b"} {
		_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: name, Requester: access.Operator})
		require.ErrorIs(t, err, ErrBadRequest, name)
	}
	assert.Empty(t, dirs(t, e.prov.Root))
}

func TestSpawnOverAConfiguredAgentsNameChangesNothing(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	require.NoError(t, e.reg.Add(core.Profile{Name: "boss"}))

	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "boss", Requester: access.Operator})
	require.ErrorIs(t, err, registry.ErrDuplicate)
	assert.Empty(t, dirs(t, e.prov.Root), "the workdir made for the refused agent is removed")
	assert.Empty(t, e.channels.bound)
	_, _, ok := e.reg.Lookup("boss")
	assert.True(t, ok)
	assert.Empty(t, e.reg.Temps())
	assert.NotContains(t, e.ev.all(), "start:boss")
}

func TestSpawnOverALiveTemporaryAgentLeavesItAlone(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	first := e.spawn(t, "tmp-a")
	require.NoError(t, os.WriteFile(filepath.Join(first.Workdir, "work.txt"), []byte("mine"), 0o600))

	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(first.Workdir, "work.txt"), "the live agent's files are untouched")
	assert.Len(t, e.reg.Temps(), 1)
}

func TestSpawnLaunchFailureUndoesEverything(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) { e.starter.fail = errors.New("tmux broke") })
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)

	assert.Empty(t, e.reg.Temps())
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.channels.bound)
	assert.Equal(t, []string{"bind:tmp-a:local", "start:tmp-a", "kill:tmp-a", "stop-follow:tmp-a", "unbind:tmp-a"}, e.ev.all(), "session, follower and binding are taken back too")
	_, err = e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	assert.Error(t, err, "still failing, but not because of leftovers")
	assert.NotContains(t, err.Error(), "already exists")
}

func TestSpawnThatNeverBecomesReadyKeepsTheAgentForTheOperator(t *testing.T) {
	notReady := &control.ReadinessError{Agent: "tmp-a", Timeout: time.Second}
	e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) { e.starter.fail = notReady })

	p, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)
	assert.ErrorAs(t, err, &notReady, "the readiness error is still the cause")
	assert.Contains(t, err.Error(), "killing it removes it")
	assert.Equal(t, "tmp-a", p.Name, "the name comes back so the operator can find it")

	_, ok := e.reg.TempOf("tmp-a")
	assert.True(t, ok, "registered: an agent is removed only by an explicit kill")
	assert.DirExists(t, p.Workdir)
	assert.NotContains(t, e.ev.all(), "kill:tmp-a", "its session is left running to inspect")
}

func TestSpawnWithNoWayToLocateTheTranscriptUndoesTheWorkdir(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Locate = func(string, string) (string, bool) { return "", false }
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())
	assert.Empty(t, e.ev.all(), "nothing was started")
}

func TestAnAgentCannotSpawnByDefault(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Agent("boss")})
	require.ErrorIs(t, err, ErrNotFound)
	assert.Empty(t, e.gate.got, "the gate is not even asked: access said no first")
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())

	_, err = e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Subject{}})
	require.ErrorIs(t, err, ErrNotFound, "nobody is not allowed either")
}

func TestAnAgentSpawnPassesAccessThenTheGate(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) { e.gate.allow = true })
	e.m.Auth = allowAgents{}

	p, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Agent("boss")})
	require.NoError(t, err)
	assert.Equal(t, "a", p.Name)
	require.Len(t, e.gate.got, 1)
	assert.Equal(t, "boss", e.gate.got[0].Agent)
	assert.Equal(t, "pao.spawn", e.gate.got[0].Tool)
	assert.JSONEq(t, `{"recipe":"researcher","agent":"a"}`, string(e.gate.got[0].Input))
}

func TestAGateDenialStopsAnAgentSpawn(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Auth = allowAgents{}
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Agent("boss")})
	require.ErrorIs(t, err, ErrDenied)
	assert.Contains(t, err.Error(), "no thanks")
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())
}

func TestNoGateMeansNoAgentRequestGoesThrough(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Auth = allowAgents{}
	e.m.Gate = nil
	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Agent("boss")})
	require.ErrorIs(t, err, ErrDenied)
	assert.Empty(t, e.reg.Temps())
}

// Kill ----------------------------------------------------------------

func TestKillStopsThenHarvestsThenDestroysThenUnregisters(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	p := e.spawn(t, "tmp-a")
	require.NoError(t, os.WriteFile(filepath.Join(p.Workdir, "result.txt"), []byte("the answer"), 0o600))

	var harvestsWhenStopped int
	var workdirWhenStopped bool
	e.plane.atKill = func(core.Profile) {
		list, _ := e.store.List(access.Operator)
		harvestsWhenStopped = len(list)
		_, err := os.Stat(filepath.Join(p.Workdir, "result.txt"))
		workdirWhenStopped = err == nil
	}

	res, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err)

	assert.Zero(t, harvestsWhenStopped, "the session is stopped before anything is copied")
	assert.True(t, workdirWhenStopped, "and the workdir is intact when it is stopped")
	assert.True(t, res.Harvest.Complete)
	assert.False(t, res.WorkdirKept)
	got, err := e.store.Read(access.Operator, res.Harvest.ID, "result.txt")
	require.NoError(t, err)
	assert.Equal(t, "the answer", string(got), "the agent's output survived its teardown")
	assert.NoDirExists(t, p.Workdir)
	_, ok := e.reg.TempOf("tmp-a")
	assert.False(t, ok)
	assert.Empty(t, e.channels.bound)
	assert.Equal(t, []string{"bind:tmp-a:local", "start:tmp-a", "kill:tmp-a", "stop-follow:tmp-a", "unbind:tmp-a"}, e.ev.all())
	assert.Equal(t, "operator", res.Harvest.Requester)
	assert.Equal(t, []string{"research"}, res.Harvest.Tags)
}

func TestKillReportsTheHarvestToWhoeverAsked(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.spawn(t, "tmp-a")
	res, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err)

	require.Len(t, *e.reports, 1)
	assert.Equal(t, access.Operator, (*e.reports)[0].who)
	assert.Contains(t, (*e.reports)[0].text, res.Harvest.ID)
	assert.Contains(t, (*e.reports)[0].text, "README.md")
}

func TestKillOfAnUnknownOrConfiguredAgentTouchesNothing(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	require.NoError(t, e.reg.Add(core.Profile{Name: "boss"}))

	for _, name := range []string{"missing", "boss", ""} {
		_, err := e.m.Kill(context.Background(), KillRequest{Name: name, Requester: access.Operator})
		require.ErrorIs(t, err, ErrNotFound, name)
	}
	assert.Empty(t, e.ev.all(), "no session was stopped")
	_, _, ok := e.reg.Lookup("boss")
	assert.True(t, ok)
}

func TestAFailedStopDestroysNothing(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	p := e.spawn(t, "tmp-a")
	e.plane.fail = errors.New("tmux says no")

	_, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)

	assert.DirExists(t, p.Workdir)
	_, ok := e.reg.TempOf("tmp-a")
	assert.True(t, ok)
	list, _ := e.store.List(access.Operator)
	assert.Empty(t, list)
	assert.Empty(t, *e.reports)
}

func TestAFailedHarvestKeepsEverythingAndTheKillCanBeRetried(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	p := e.spawn(t, "tmp-a")
	locked := filepath.Join(p.Workdir, "locked")
	require.NoError(t, os.WriteFile(locked, []byte("x"), 0o000))

	_, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can be retried")

	assert.FileExists(t, locked, "nothing was destroyed")
	v, ok := e.reg.TempOf("tmp-a")
	require.True(t, ok, "still registered")
	assert.Equal(t, core.Stopped, v.State)
	list, _ := e.store.List(access.Operator)
	assert.Empty(t, list, "no half harvest is listed")
	assert.Empty(t, *e.reports)

	require.NoError(t, os.Chmod(locked, 0o600))
	res, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err, "the retry works")
	assert.True(t, res.Harvest.Complete)
	assert.NoDirExists(t, p.Workdir)
}

func TestAnIncompleteHarvestKeepsTheWorkdirAndSaysWhere(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) {
		e.store = harvest.New(filepath.Join(filepath.Dir(e.prov.Root), "harvest"), nil, harvest.WithLimits(1, 1<<20))
	})
	p := e.spawn(t, "tmp-a")
	require.NoError(t, os.WriteFile(filepath.Join(p.Workdir, "second.txt"), []byte("more"), 0o600))

	res, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err)

	assert.False(t, res.Harvest.Complete)
	assert.True(t, res.WorkdirKept)
	assert.DirExists(t, p.Workdir, "work that was not captured is not destroyed")
	_, ok := e.reg.TempOf("tmp-a")
	assert.False(t, ok, "the agent is gone; only its files remain")
	require.Len(t, *e.reports, 1)
	assert.Contains(t, (*e.reports)[0].text, "INCOMPLETE")
	assert.Contains(t, (*e.reports)[0].text, p.Workdir)

	_, err = e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "tmp-a", Requester: access.Operator})
	require.Error(t, err, "the name stays taken until the operator deals with the kept files")
	assert.DirExists(t, p.Workdir)
}

func TestAnAgentKillIsDeniedByDefaultAndLooksLikeNoSuchAgent(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	p := e.spawn(t, "tmp-a")

	_, denied := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Agent("boss")})
	_, missing := e.m.Kill(context.Background(), KillRequest{Name: "no-such", Requester: access.Agent("boss")})
	assert.Equal(t, missing, denied, "a refusal does not reveal that the agent exists")
	_, zero := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Subject{}})
	assert.ErrorIs(t, zero, ErrNotFound)

	assert.Empty(t, e.gate.got, "access said no, so the gate is not asked")
	assert.NotContains(t, e.ev.all(), "kill:tmp-a")
	assert.DirExists(t, p.Workdir)
	_, ok := e.reg.TempOf("tmp-a")
	assert.True(t, ok)
}

func TestAnAgentKillThatAccessAllowsStillNeedsTheGate(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Auth = allowAgents{}
	p := e.spawn(t, "tmp-a")

	_, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Agent("boss")})
	require.ErrorIs(t, err, ErrDenied)
	assert.Contains(t, err.Error(), "no thanks")
	require.Len(t, e.gate.got, 1)
	assert.Equal(t, "boss", e.gate.got[0].Agent)
	assert.Equal(t, "pao.kill", e.gate.got[0].Tool)
	assert.JSONEq(t, `{"agent":"tmp-a"}`, string(e.gate.got[0].Input))
	assert.DirExists(t, p.Workdir)
	assert.NotContains(t, e.ev.all(), "kill:tmp-a")
	list, _ := e.store.List(access.Operator)
	assert.Empty(t, list)
}

func TestAnApprovedAgentKillHarvestsAndTellsTheAgent(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML}, func(e *env) { e.gate.allow = true })
	e.m.Auth = allowAgents{}
	e.spawn(t, "tmp-a")

	res, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Agent("boss")})
	require.NoError(t, err)

	assert.Equal(t, "agent:boss", res.Harvest.Requester)
	require.Len(t, *e.reports, 1)
	assert.Equal(t, access.Agent("boss"), (*e.reports)[0].who, "the harvest goes to whoever asked, here the agent")
	_, ok := e.reg.TempOf("tmp-a")
	assert.False(t, ok)
}

func TestTwoKillsOfOneAgentHarvestOnce(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.spawn(t, "tmp-a")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, countNil(errs), "exactly one kill succeeds")
	for _, err := range errs {
		if err != nil {
			assert.ErrorIs(t, err, ErrNotFound)
		}
	}
	list, _ := e.store.List(access.Operator)
	assert.Len(t, list, 1)
}

func countNil(errs []error) int {
	n := 0
	for _, e := range errs {
		if e == nil {
			n++
		}
	}
	return n
}

func TestAKilledNameCanBeSpawnedAgain(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.spawn(t, "tmp-a")
	first, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err)
	e.spawn(t, "tmp-a")
	second, err := e.m.Kill(context.Background(), KillRequest{Name: "tmp-a", Requester: access.Operator})
	require.NoError(t, err)
	assert.NotEqual(t, first.Harvest.ID, second.Harvest.ID, "each life of a name has its own harvest")
}

// List ----------------------------------------------------------------

func TestListShowsTheFlagAndNeverKills(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.spawn(t, "b")
	e.spawn(t, "a")

	got := e.m.List()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Name)
	assert.Equal(t, registry.FlagActive, got[0].Flag)
	assert.Equal(t, "researcher", got[0].Recipe)
	assert.Equal(t, "local", got[0].Channel)

	*e.now = e.now.Add(11 * time.Minute)
	e.reg.Touch("b", *e.now, true)
	got = e.m.List()
	assert.Equal(t, registry.FlagUnused, got[0].Flag, "never given a message")
	assert.Equal(t, registry.FlagActive, got[1].Flag)

	*e.now = e.now.Add(24 * time.Hour)
	got = e.m.List()
	assert.Equal(t, registry.FlagUnused, got[0].Flag)
	assert.Equal(t, registry.FlagIdle, got[1].Flag)
	assert.Len(t, e.reg.Temps(), 2, "flagging never removes an agent")
	assert.NotContains(t, e.ev.all(), "kill:a")
	assert.NotContains(t, e.ev.all(), "kill:b")
}

func TestListUsesConfiguredThresholds(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Thresholds = registry.Thresholds{UnusedAfter: time.Minute, IdleAfter: time.Minute}
	e.spawn(t, "a")
	*e.now = e.now.Add(2 * time.Minute)
	assert.Equal(t, registry.FlagUnused, e.m.List()[0].Flag)
}

func TestSpawnRefusesAProfileTheNodeCannotRunBeforeStartingAnything(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Check = func(p core.Profile) error { return errors.New("no reader for " + p.Transcript.Format) }

	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Operator})
	require.ErrorIs(t, err, ErrBadRequest)
	assert.Contains(t, err.Error(), "no reader for harness-jsonl")
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())
	assert.Empty(t, e.ev.all())
}

func TestAnUnknownRecipeLooksLikeADeniedOneToAnyoneButTheOperator(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	e.m.Auth = allowAgents{}
	e.gate.allow = true

	_, unknown := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "nope", Name: "a", Requester: access.Agent("boss")})
	e.m.Auth = nil
	_, denied := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: "a", Requester: access.Agent("boss")})
	assert.Equal(t, denied, unknown, "an agent cannot tell a recipe that does not exist from one it may not use")

	_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "nope", Name: "a", Requester: access.Subject{}})
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = e.m.Spawn(context.Background(), SpawnRequest{Recipe: "nope", Name: "a", Requester: access.Operator})
	assert.ErrorIs(t, err, ErrNoRecipe, "the operator is told which")
}

func TestSpawnRefusesTheNamesPaoSpeaksAs(t *testing.T) {
	e := newEnv(t, map[string]string{"researcher": recipeYAML})
	for _, name := range core.ReservedNames {
		_, err := e.m.Spawn(context.Background(), SpawnRequest{Recipe: "researcher", Name: name, Requester: access.Operator})
		require.ErrorIs(t, err, ErrBadRequest, name)
	}
	assert.Empty(t, dirs(t, e.prov.Root))
	assert.Empty(t, e.reg.Temps())
}
