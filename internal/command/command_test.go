package command

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// fakeOps records each call it is given.
type fakeOps struct {
	calls    []string
	err      error
	agents   []Agent
	bindings []Binding
	// spawned is the name Spawn returns.
	spawned string
	killed  Killed
	// hold, when set, keeps Spawn waiting until it is closed; spawnCtx is its
	// context's error then.
	hold     chan struct{}
	spawnCtx error
	// untilDone keeps Spawn waiting until its context ends.
	untilDone bool
	// file is what File returns.
	file File
}

func (f *fakeOps) Agents(context.Context) ([]Agent, error) {
	f.calls = append(f.calls, "agents")
	return f.agents, f.err
}

func (f *fakeOps) SetStream(_ context.Context, agent, channel string, level int) error {
	f.calls = append(f.calls, strings.Join([]string{"stream", agent, channel, itoa(level)}, "|"))
	return f.err
}

func (f *fakeOps) View(_ context.Context, agent, action string) error {
	f.calls = append(f.calls, "view|"+agent+"|"+action)
	return f.err
}

func (f *fakeOps) Decide(_ context.Context, id, decision, reason string) error {
	f.calls = append(f.calls, "decide|"+id+"|"+decision+"|"+reason)
	return f.err
}

func (f *fakeOps) Bind(_ context.Context, b Binding) (Binding, error) {
	f.calls = append(f.calls, strings.Join([]string{"bind", b.Agent, b.Stream, b.To, b.Level, b.Prefix}, "|"))
	if b.Stream == "" {
		b.Stream = "stdout"
	}
	b.From = "runtime"
	return b, f.err
}

func (f *fakeOps) Unbind(_ context.Context, b Binding) (Binding, error) {
	f.calls = append(f.calls, strings.Join([]string{"unbind", b.Agent, b.Stream, b.To}, "|"))
	return b, f.err
}

func (f *fakeOps) Bindings(_ context.Context, agent string) ([]Binding, error) {
	f.calls = append(f.calls, "bindings|"+agent)
	return f.bindings, f.err
}

func (f *fakeOps) Spawn(ctx context.Context, recipe, name, channel string) (string, error) {
	f.calls = append(f.calls, strings.Join([]string{"spawn", recipe, name, channel}, "|"))
	if f.hold != nil {
		<-f.hold
		f.spawnCtx = ctx.Err()
	}
	if f.untilDone {
		<-ctx.Done()
	}
	return f.spawned, f.err
}

func (f *fakeOps) Kill(_ context.Context, name string) (Killed, error) {
	f.calls = append(f.calls, "kill|"+name)
	return f.killed, f.err
}

func (f *fakeOps) File(_ context.Context, agent, path string) (File, error) {
	f.calls = append(f.calls, "file|"+agent+"|"+path)
	return f.file, f.err
}

func itoa(n int) string { return string(rune('0' + n)) }

var (
	operator = Caller{Channel: "tg", Operator: true}
	other    = Caller{Channel: "tg"}
)

func TestIs(t *testing.T) {
	for text, want := range map[string]bool{
		"/pao":                true,
		"/pao help":           true,
		"  /pao stream a":     true,
		"/pao@mybot help":     true,
		"/pao\nhelp":          true,
		"/paohelp":            false,
		"pao help":            false,
		"hello /pao":          false,
		"@agent hi":           false,
		"/allow q1":           false,
		"":                    false,
		"/pao-something else": false,
	} {
		assert.Equal(t, want, Is(text), "%q", text)
	}
}

func TestTableMatchesReservedWords(t *testing.T) {
	got := Words()
	slices.Sort(got)
	want := slices.Clone(core.CommandWords)
	slices.Sort(want)
	assert.Equal(t, want, got, "every command word must be reserved as an agent name, and only those")
}

func TestHelpIsFilteredByWhoAsks(t *testing.T) {
	all := Help(operator)
	for _, s := range table {
		assert.Contains(t, all, usage(s), "the operator's help lists %q", s.word)
	}
	some := Help(other)
	for _, s := range table {
		if s.operator {
			assert.NotContains(t, some, Prefix+" "+s.word+" ", "help for a non-operator lists %q", s.word)
		} else {
			assert.Contains(t, some, usage(s))
		}
	}
	assert.Equal(t, all, Run(context.Background(), &fakeOps{}, operator, "/pao").Reply)
	assert.Equal(t, all, Run(context.Background(), &fakeOps{}, operator, "/pao help").Reply)
	assert.Equal(t, "usage: /pao help", Run(context.Background(), &fakeOps{}, operator, "/pao help me").Reply)
}

func TestAddressing(t *testing.T) {
	for text, want := range map[string]core.Target{
		"/pao agent coder fix the build":         {Kind: core.ByName, Value: "coder"},
		"/pao tag ops fix the build":             {Kind: core.ByTag, Value: "ops"},
		"/pao coder fix the build":               {Kind: core.ByName, Value: "coder"},
		"/pao@bot coder fix the build":           {Kind: core.ByName, Value: "coder"},
		"/pao agent coder\nfix the build":        {Kind: core.ByName, Value: "coder"},
		"  /pao   agent   coder   fix the build": {Kind: core.ByName, Value: "coder"},
	} {
		for _, c := range []Caller{operator, other} {
			ops := &fakeOps{}
			res := Run(context.Background(), ops, c, text)
			require.NotNil(t, res.Address, "%q", text)
			assert.Equal(t, want, *res.Address, "%q", text)
			assert.Equal(t, "fix the build", res.Text, "%q", text)
			assert.Empty(t, res.Reply)
			assert.Empty(t, ops.calls)
		}
	}
	assert.Equal(t, "line one\n  line two", Run(context.Background(), &fakeOps{}, operator, "/pao agent coder line one\n  line two").Text)

	for _, text := range []string{"/pao agent", "/pao agent coder", "/pao tag ops", "/pao tag"} {
		res := Run(context.Background(), &fakeOps{}, operator, text)
		assert.Nil(t, res.Address, "%q", text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: /pao "), "%q: %q", text, res.Reply)
	}
	res := Run(context.Background(), &fakeOps{}, operator, "/pao coder")
	assert.Nil(t, res.Address)
	assert.Contains(t, res.Reply, `unknown command "coder"`)
}

func TestStream(t *testing.T) {
	for text, want := range map[string]string{
		"/pao stream coder":         "stream|coder|tg|0",
		"/pao stream coder 2":       "stream|coder|tg|2",
		"/pao stream coder local":   "stream|coder|local|0",
		"/pao stream coder local 3": "stream|coder|local|3",
		"/pao stream coder --clear": "stream|coder||0",
	} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Equal(t, []string{want}, ops.calls, "%q", text)
		assert.NotEmpty(t, res.Reply)
		assert.Nil(t, res.Address)
	}
	for _, text := range []string{"/pao stream", "/pao stream coder local x", "/pao stream coder --nope", "/pao stream a b 1 2", "/pao stream coder --clear 2"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: /pao stream"), "%q: %q", text, res.Reply)
	}
}

func TestViewAndDecide(t *testing.T) {
	for text, want := range map[string]string{
		"/pao view coder pause":            "view|coder|pause",
		"/pao view coder resume":           "view|coder|resume",
		"/pao allow q3":                    "decide|q3|allow|",
		"/pao always s2.q3":                "decide|s2.q3|always|",
		"/pao deny q3":                     "decide|q3|deny|",
		"/pao deny q3 not on main, please": "decide|q3|deny|not on main, please",
	} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Equal(t, []string{want}, ops.calls, "%q", text)
		assert.NotEmpty(t, res.Reply)
	}
	for _, text := range []string{"/pao view coder", "/pao view coder stop", "/pao allow", "/pao allow q3 because", "/pao deny"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: "), "%q: %q", text, res.Reply)
	}
}

func TestAgents(t *testing.T) {
	ops := &fakeOps{agents: []Agent{
		{Name: "wasp", State: "stopped"},
		{Name: "bee", Tags: []string{"ops", "team"}, State: "running"},
	}}
	res := Run(context.Background(), ops, operator, "/pao agents")
	assert.Equal(t, "agents you may reach:\nbee: running; tags ops, team\nwasp: stopped", res.Reply, "sorted by name, tags only where there are some")
	assert.Nil(t, res.Address)
	assert.Equal(t, []string{"agents"}, ops.calls)
	assert.Equal(t, "wasp", ops.agents[0].Name, "the operation's own list is not reordered")

	assert.Equal(t, "no agents you may reach", Run(context.Background(), &fakeOps{}, operator, "/pao agents").Reply)

	ops = &fakeOps{}
	assert.Equal(t, "usage: /pao agents", Run(context.Background(), ops, operator, "/pao agents bee").Reply)
	assert.Empty(t, ops.calls)

	assert.Contains(t, Run(context.Background(), ops, other, "/pao agents").Reply, `unknown command "agents"`, "operator-only in the first build")
	assert.NotContains(t, Help(other), Prefix+" agents")
	assert.Empty(t, ops.calls)
}

// An operation's failure is the reply, in the operation's own words, as the
// operator surface reports it.
func TestFailureIsTheOperationsOwnWords(t *testing.T) {
	ops := &fakeOps{err: errors.New(`no agent "coder"`)}
	for _, text := range []string{"/pao stream coder", "/pao view coder pause", "/pao allow q1", "/pao agents"} {
		assert.Equal(t, `no agent "coder"`, Run(context.Background(), ops, operator, text).Reply, "%q", text)
	}
}

// A steering command from a caller who is not the operator is answered exactly
// as a word that is no command at all, and nothing is run.
func TestOperatorCommandsRefusedAsUnknown(t *testing.T) {
	for _, s := range table {
		if !s.operator {
			continue
		}
		ops := &fakeOps{}
		got := Run(context.Background(), ops, other, "/pao "+s.word)
		assert.Empty(t, ops.calls, s.word)
		assert.Equal(t, strings.ReplaceAll(Run(context.Background(), ops, other, "/pao nosuchword").Reply, "nosuchword", s.word), got.Reply, s.word)
	}
}

func TestBind(t *testing.T) {
	for text, want := range map[string]struct{ call, reply string }{
		"/pao bind coder group:team":                 {"bind|coder||group:team||", "group:team follows coder's stdout"},
		"/pao bind coder group:team 2":               {"bind|coder||group:team|2|", "group:team follows coder's stdout at level 2"},
		"/pao bind coder group:team log":             {"bind|coder||group:team|log|", "group:team follows coder's stdout at level log"},
		"/pao bind coder group:team 2 TA:":           {"bind|coder||group:team|2|TA:", `group:team follows coder's stdout at level 2, prefix "TA:"`},
		"/pao bind coder group:team Team A:":         {"bind|coder||group:team||Team A:", `group:team follows coder's stdout, prefix "Team A:"`},
		"/pao bind coder private:alice stderr":       {"bind|coder|stderr|private:alice||", "private:alice follows coder's stderr"},
		"/pao bind coder private:alice stdout 3 C:":  {"bind|coder|stdout|private:alice|3|C:", `private:alice follows coder's stdout at level 3, prefix "C:"`},
		"/pao bind coder private:alice stderr 3":     {"bind|coder|stderr|private:alice|3|", "private:alice follows coder's stderr at level 3"},
		"  /pao   bind   coder   group:team   2   T": {"bind|coder||group:team|2|T", `group:team follows coder's stdout at level 2, prefix "T"`},
	} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Equal(t, []string{want.call}, ops.calls, "%q", text)
		assert.Equal(t, want.reply, res.Reply, "%q", text)
		assert.Nil(t, res.Address)
	}
	for _, text := range []string{"/pao bind", "/pao bind coder"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: /pao bind "), "%q: %q", text, res.Reply)
	}
	res := Run(context.Background(), &fakeOps{err: errors.New("no admitted group is named \"x\"")}, operator, "/pao bind coder group:x")
	assert.Equal(t, `no admitted group is named "x"`, res.Reply, "a refusal is said as the operation says it")
}

func TestUnbind(t *testing.T) {
	for text, want := range map[string]struct{ call, reply string }{
		"/pao unbind coder group:team":           {"unbind|coder||group:team", "group:team no longer follows coder's stdout"},
		"/pao unbind coder private:alice stderr": {"unbind|coder|stderr|private:alice", "private:alice no longer follows coder's stderr"},
		"/pao unbind coder group:team stdout":    {"unbind|coder|stdout|group:team", "group:team no longer follows coder's stdout"},
	} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Equal(t, []string{want.call}, ops.calls, "%q", text)
		assert.Equal(t, want.reply, res.Reply, "%q", text)
	}
	for _, text := range []string{"/pao unbind", "/pao unbind coder", "/pao unbind coder group:team stdin", "/pao unbind coder group:team stderr x"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, operator, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: /pao unbind "), "%q: %q", text, res.Reply)
	}
}

func TestBindings(t *testing.T) {
	ops := &fakeOps{bindings: []Binding{
		{Agent: "coder", Stream: "stdout", To: "group:team", Level: "2", From: "config"},
		{Agent: "coder", Stream: "stderr", To: "private:alice", From: "runtime"},
		{Agent: "coder", Stream: "stdout", To: "channel:local", Level: "log", Prefix: "C:", From: "runtime"},
	}}
	res := Run(context.Background(), ops, operator, "/pao bindings coder")
	assert.Equal(t, []string{"bindings|coder"}, ops.calls)
	assert.Equal(t, "standing bindings:\n"+
		"group:team follows coder's stdout at level 2 (config)\n"+
		"private:alice follows coder's stderr (runtime)\n"+
		`channel:local follows coder's stdout at level log, prefix "C:" (runtime)`, res.Reply)

	ops = &fakeOps{}
	res = Run(context.Background(), ops, operator, "/pao bindings")
	assert.Equal(t, []string{"bindings|"}, ops.calls)
	assert.Equal(t, "no standing bindings", res.Reply)

	res = Run(context.Background(), &fakeOps{}, operator, "/pao bindings a b")
	assert.True(t, strings.HasPrefix(res.Reply, "usage: /pao bindings"), res.Reply)
}

func TestBindingCommandsAreTheOperatorsOnly(t *testing.T) {
	for _, text := range []string{"/pao bind coder group:team", "/pao unbind coder group:team", "/pao bindings"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, other, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.Contains(t, res.Reply, "unknown command", "%q", text)
	}
}

// laterCaller is the operator, with a later answer that lands on the channel
// returned.
func laterCaller() (Caller, <-chan string) {
	got := make(chan string, 1)
	c := operator
	c.Later = func(_ context.Context, text string) { got <- text }
	return c, got
}

func TestSpawnIsAnsweredAtOnceAndToldItsOutcomeLater(t *testing.T) {
	for name, c := range map[string]struct {
		text    string
		spawned string
		err     error
		call    string
		later   string
	}{
		"ready": {"/pao spawn helper", "helper-1", nil, "spawn|helper||tg",
			"helper-1 is ready, spawned from recipe helper"},
		"named": {"/pao spawn helper scout", "scout", nil, "spawn|helper|scout|tg",
			"scout is ready, spawned from recipe helper"},
		"not ready": {"/pao spawn helper", "helper-1", errors.New("no prompt after 60s"), "spawn|helper||tg",
			"helper-1 was created from recipe helper but is not ready: no prompt after 60s\nkill it with /pao kill helper-1"},
		"failed": {"/pao spawn nope", "", errors.New(`no recipe "nope"`), "spawn|nope||tg",
			`spawn from recipe nope failed: no recipe "nope"`},
	} {
		ops := &fakeOps{spawned: c.spawned, err: c.err}
		caller, later := laterCaller()
		res := Run(context.Background(), ops, caller, c.text)
		assert.Contains(t, res.Reply, "spawning an agent from recipe", name)
		select {
		case got := <-later:
			assert.Equal(t, c.later, got, name)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no later answer", name)
		}
		assert.Equal(t, []string{c.call}, ops.calls, "%s: spawned on the caller's channel", name)
	}
}

func TestSpawnRunsAfterTheCommandsContextEnds(t *testing.T) {
	ops := &fakeOps{spawned: "helper-1", hold: make(chan struct{})}
	caller, later := laterCaller()
	ctx, cancel := context.WithCancel(context.Background())
	Run(ctx, ops, caller, "/pao spawn helper")
	cancel()
	close(ops.hold)
	assert.Equal(t, "helper-1 is ready, spawned from recipe helper", <-later)
	assert.NoError(t, ops.spawnCtx, "the spawn's context outlives the command's")
}

func TestASpawnThatRunsOutItsTimeIsStillTold(t *testing.T) {
	defer func(d time.Duration) { spawnTimeout = d }(spawnTimeout)
	spawnTimeout = 50 * time.Millisecond
	ops := &fakeOps{spawned: "helper-1", err: context.DeadlineExceeded, untilDone: true}
	caller, _ := laterCaller()
	told := make(chan error, 1)
	caller.Later = func(ctx context.Context, _ string) { told <- ctx.Err() }
	Run(context.Background(), ops, caller, "/pao spawn helper")
	select {
	case err := <-told:
		assert.NoError(t, err, "the outcome is sent on a context the spawn's bound did not end")
	case <-time.After(5 * time.Second):
		t.Fatal("no later answer")
	}
}

func TestSpawnAndKillUsage(t *testing.T) {
	caller, _ := laterCaller()
	for _, text := range []string{"/pao spawn", "/pao spawn a b c", "/pao kill", "/pao kill a b"} {
		ops := &fakeOps{}
		res := Run(context.Background(), ops, caller, text)
		assert.True(t, strings.HasPrefix(res.Reply, "usage: "), "%q: %s", text, res.Reply)
		assert.Empty(t, ops.calls, "%q", text)
	}
	ops := &fakeOps{}
	res := Run(context.Background(), ops, operator, "/pao spawn helper")
	assert.Equal(t, "spawn is not available on this channel", res.Reply, "no later answer, no spawn")
	assert.Empty(t, ops.calls)
}

func TestKill(t *testing.T) {
	ops := &fakeOps{killed: Killed{Harvest: "h1"}}
	assert.Equal(t, "scout is killed; its outputs are harvested as h1", Run(context.Background(), ops, operator, "/pao kill scout").Reply)
	assert.Equal(t, []string{"kill|scout"}, ops.calls)

	ops = &fakeOps{killed: Killed{Harvest: "h2", Report: "harvested 1 file(s)", WorkdirKept: true}}
	assert.Equal(t, "scout is killed; its outputs are harvested as h2\nharvested 1 file(s)\nthe workdir was kept",
		Run(context.Background(), ops, operator, "/pao kill scout").Reply)

	ops = &fakeOps{err: errors.New(`"coder" is not a temporary agent`)}
	assert.Equal(t, `"coder" is not a temporary agent`, Run(context.Background(), ops, operator, "/pao kill coder").Reply)
}

func TestSpawnAndKillAreTheOperatorsOnly(t *testing.T) {
	for _, text := range []string{"/pao spawn helper", "/pao kill scout"} {
		ops := &fakeOps{}
		c := other
		c.Later = func(context.Context, string) { t.Errorf("%q told a non-operator", text) }
		res := Run(context.Background(), ops, c, text)
		assert.Empty(t, ops.calls, "%q", text)
		assert.Contains(t, res.Reply, "unknown command", "%q", text)
	}
}

func TestProposable(t *testing.T) {
	for _, text := range []string{
		"/pao bind coder group:team 2 TA:",
		"  /pao agent coder fix the build  ",
		"/pao stream coder local 2",
		"/pao view coder pause",
		"/pao unbind coder group:team",
		"/pao agents",
		"/pao spawn helper scout",
		"/pao kill scout",
	} {
		got, err := Proposable(text, nil)
		require.NoError(t, err, "%q", text)
		assert.Equal(t, strings.TrimSpace(text), got)
	}
	for text, why := range map[string]string{
		"/pao help":                  "not a command a governor may propose",
		"/pao allow q1":              "not a command a governor may propose",
		"/pao always q1":             "not a command a governor may propose",
		"/pao deny q1":               "not a command a governor may propose",
		"/pao coder fix it":          "not a command a governor may propose",
		"/pao do bind coder":         "not a command a governor may propose",
		"/pao":                       "not a command a governor may propose",
		"/pao@bot agents":            "begins",
		"pao agents":                 "begins",
		"bind coder group:team":      "begins",
		"/pao agent coder a\nb":      "one line",
		"/pao agents\n/pao allow q1": "one line",
	} {
		_, err := Proposable(text, nil)
		require.Error(t, err, "%q", text)
		assert.Contains(t, err.Error(), why, "%q", text)
	}
}

func TestFileSendsAFileToWhoeverMayAddressTheAgent(t *testing.T) {
	ops := &fakeOps{file: File{Name: "a b.md", Data: []byte("x")}}
	for _, c := range []Caller{operator, other} {
		res := Run(context.Background(), ops, c, "/pao file vault notes/a b.md")
		require.NotNil(t, res.File, "not for the operator alone (ADR-0052 point 2)")
		assert.Equal(t, "a b.md", res.File.Name)
		assert.Equal(t, "notes/a b.md from vault", res.Reply)
	}
	assert.Equal(t, []string{"file|vault|notes/a b.md", "file|vault|notes/a b.md"}, ops.calls, "the path is the rest of the line, spaces kept")

	ops = &fakeOps{err: errors.New("no file \"x\" for agent \"vault\" that you may have")}
	res := Run(context.Background(), ops, operator, "/pao file vault x")
	assert.Nil(t, res.File)
	assert.Equal(t, ops.err.Error(), res.Reply, "the refusal is the operation's own words")

	res = Run(context.Background(), &fakeOps{}, operator, "/pao file vault")
	assert.Equal(t, "usage: /pao file <agent> <path>", res.Reply)
	assert.Contains(t, Help(other), "/pao file", "listed for a caller who is not the operator too")
}
