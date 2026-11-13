package gate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAsker struct {
	mu    sync.Mutex
	calls []string
	ans   Answer
	err   error
	block bool
}

func (f *fakeAsker) Ask(ctx context.Context, req Request, canon string) (Answer, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req.Tool+" "+canon)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}
	return f.ans, f.err
}

func (f *fakeAsker) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeJudge struct {
	v      Verdict
	reason string
	err    error
	calls  int
}

func (f *fakeJudge) Judge(context.Context, Request) (Verdict, string, error) {
	f.calls++
	return f.v, f.reason, f.err
}

func newPolicy(t *testing.T, stored string) (*Policy, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if stored != "" {
		require.NoError(t, os.WriteFile(path, []byte(stored), 0o600))
	}
	p, err := LoadPolicy(path)
	require.NoError(t, err)
	return p, path
}

func req(tool, input string) Request {
	return Request{Agent: "a", Tool: tool, Input: json.RawMessage(input)}
}

func newGate(t *testing.T, stored string, judge Judge, asker Asker) (*Gate, string) {
	t.Helper()
	p, path := newPolicy(t, stored)
	return &Gate{Policy: p, Judge: judge, Asker: asker, AutoAllow: DefaultAutoAllow(), AskTimeout: time.Second}, path
}

func TestReadActionsAutoAllowWithoutAsking(t *testing.T) {
	asker := &fakeAsker{}
	g, _ := newGate(t, "", nil, asker)

	d := g.Decide(context.Background(), req("Read", `{"file_path":"/x"}`))
	assert.True(t, d.Allow)
	assert.Equal(t, "rule", d.Via)
	assert.Empty(t, asker.asked())
}

func TestUncoveredActionFailsClosedWithoutAnOperator(t *testing.T) {
	g, _ := newGate(t, "", nil, nil)

	d := g.Decide(context.Background(), req("Bash", `{"command":"rm -rf /"}`))
	assert.False(t, d.Allow, "an action no rule covers is never silently allowed")
	assert.Equal(t, "closed", d.Via)
}

func TestUncoveredActionGoesToTheOperator(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: true}}
	g, _ := newGate(t, "", nil, asker)

	d := g.Decide(context.Background(), req("Bash", `{"command":"ls"}`))
	assert.True(t, d.Allow)
	assert.Equal(t, "operator", d.Via)
	assert.Equal(t, []string{`Bash {"command":"ls"}`}, asker.asked())
}

func TestOperatorDenialCarriesItsReason(t *testing.T) {
	g, _ := newGate(t, "", nil, &fakeAsker{ans: Answer{Allow: false, Reason: "not on prod"}})

	d := g.Decide(context.Background(), req("Bash", `{"command":"deploy"}`))
	assert.False(t, d.Allow)
	assert.Equal(t, "not on prod", d.Reason)

	g.Asker = &fakeAsker{ans: Answer{Allow: false}}
	d = g.Decide(context.Background(), req("Bash", `{"command":"deploy"}`))
	assert.Equal(t, "denied by the operator", d.Reason)
}

func TestAnUnanswerableOperatorIsADenial(t *testing.T) {
	g, _ := newGate(t, "", nil, &fakeAsker{err: errors.New("channel down")})
	d := g.Decide(context.Background(), req("Bash", `{"command":"ls"}`))
	assert.False(t, d.Allow)
	assert.Equal(t, "closed", d.Via)
	assert.Contains(t, d.Reason, "channel down")
}

func TestAnUnansweredQuestionTimesOutClosed(t *testing.T) {
	g, _ := newGate(t, "", nil, &fakeAsker{block: true})
	g.AskTimeout = 30 * time.Millisecond

	start := time.Now()
	d := g.Decide(context.Background(), req("Bash", `{"command":"ls"}`))
	assert.False(t, d.Allow)
	assert.Equal(t, "closed", d.Via)
	assert.Contains(t, d.Reason, "in time")
	assert.Less(t, time.Since(start), time.Second)
}

func TestMalformedInputFailsClosed(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: true}}
	g, _ := newGate(t, "", nil, asker)

	for _, in := range []string{`{`, `{"a":1} {"b":2}`} {
		d := g.Decide(context.Background(), req("Read", in))
		assert.False(t, d.Allow, "input %q", in)
		assert.Equal(t, "closed", d.Via)
	}
	assert.Empty(t, asker.asked(), "an unreadable action is not even put to the operator")
}

func TestEmptyInputIsAnEmptyObject(t *testing.T) {
	g, _ := newGate(t, "", nil, nil)
	d := g.Decide(context.Background(), Request{Tool: "Read"})
	assert.True(t, d.Allow, "a read with no arguments is still a read")
}

func TestJudgeDecidesBeforeTheOperator(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: false}}
	judge := &fakeJudge{v: VerdictAllow, reason: "benign"}
	g, _ := newGate(t, "", judge, asker)

	d := g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
	assert.True(t, d.Allow)
	assert.Equal(t, "judge", d.Via)
	assert.Empty(t, asker.asked())

	judge.v, judge.reason = VerdictDeny, ""
	d = g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
	assert.False(t, d.Allow)
	assert.Equal(t, "judge", d.Via)
	assert.Empty(t, asker.asked())
}

func TestInconclusiveOrFailedJudgeFallsToTheOperator(t *testing.T) {
	for name, judge := range map[string]*fakeJudge{
		"inconclusive": {v: Inconclusive},
		"error":        {err: errors.New("judge unreachable")},
	} {
		t.Run(name, func(t *testing.T) {
			asker := &fakeAsker{ans: Answer{Allow: true}}
			g, _ := newGate(t, "", judge, asker)

			d := g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
			assert.True(t, d.Allow)
			assert.Equal(t, "operator", d.Via)
			assert.Len(t, asker.asked(), 1)
		})
	}
}

// hangingJudge never answers, and ignores its context, like a wedged agent.
type hangingJudge struct{ release chan struct{} }

func (h hangingJudge) Judge(context.Context, Request) (Verdict, string, error) {
	<-h.release
	return VerdictAllow, "too late", nil
}

func TestAJudgeThatNeverAnswersFallsThroughToTheOperator(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	asker := &fakeAsker{ans: Answer{Allow: false, Reason: "operator says no"}}
	g, _ := newGate(t, "", hangingJudge{release}, asker)
	g.JudgeTimeout = 50 * time.Millisecond

	start := time.Now()
	d := g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
	assert.Less(t, time.Since(start), time.Second, "the decision must not wait on a wedged judge")
	assert.False(t, d.Allow)
	assert.Equal(t, "operator", d.Via)
	assert.Equal(t, "operator says no", d.Reason)
	assert.Len(t, asker.asked(), 1)
}

func TestAJudgeVerdictThatArrivesTooLateIsIgnored(t *testing.T) {
	release := make(chan struct{})
	asker := &fakeAsker{ans: Answer{Allow: false}}
	g, _ := newGate(t, "", hangingJudge{release}, asker)
	g.JudgeTimeout = 20 * time.Millisecond

	d := g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
	close(release) // the judge answers "allow" after the gate moved on
	assert.False(t, d.Allow, "a late allow from the judge must not turn into an allow")
}

func TestAJudgeErrorNeverAllows(t *testing.T) {
	g, _ := newGate(t, "", &fakeJudge{err: errors.New("x")}, nil)
	d := g.Decide(context.Background(), req("Bash", `{"command":"date"}`))
	assert.False(t, d.Allow)
}

func TestHigherRiskRuleAsksTheOperatorNotTheJudge(t *testing.T) {
	stored := `[{"tool":"Bash","match":"push","risk":"shared","source":"config"}]`
	asker := &fakeAsker{ans: Answer{Allow: true}}
	judge := &fakeJudge{v: VerdictAllow}
	g, _ := newGate(t, stored, judge, asker)

	d := g.Decide(context.Background(), req("Bash", `{"command":"git push"}`))
	assert.True(t, d.Allow)
	assert.Equal(t, "operator", d.Via)
	assert.Zero(t, judge.calls, "the policy already classified it; nothing fuzzy for a judge")
}

func TestRiskLevelsFollowTheAutoAllowSet(t *testing.T) {
	stored := `[{"tool":"Edit","risk":"private","source":"config"}]`
	asker := &fakeAsker{ans: Answer{Allow: false}}
	g, _ := newGate(t, stored, nil, asker)

	d := g.Decide(context.Background(), req("Edit", `{"file_path":"/x"}`))
	assert.False(t, d.Allow, "private is not auto-allowed by default")
	assert.Len(t, asker.asked(), 1)

	g.AutoAllow[RiskPrivate] = true
	d = g.Decide(context.Background(), req("Edit", `{"file_path":"/x"}`))
	assert.True(t, d.Allow)
	assert.Len(t, asker.asked(), 1, "once private auto-allows, it is not asked again")
}

func TestExplicitDenyBeatsALaterAllow(t *testing.T) {
	stored := `[{"tool":"Read","match":"secrets","effect":"deny","source":"config"}]`
	g, _ := newGate(t, stored, nil, &fakeAsker{ans: Answer{Allow: true}})

	d := g.Decide(context.Background(), req("Read", `{"file_path":"/etc/secrets"}`))
	assert.False(t, d.Allow, "the stored deny comes before the builtin read allow")
	assert.Equal(t, "rule", d.Via)

	d = g.Decide(context.Background(), req("Read", `{"file_path":"/etc/hosts"}`))
	assert.True(t, d.Allow)
}

func TestRuleMatchesTheSameActionWhateverTheKeyOrder(t *testing.T) {
	stored := `[{"tool":"Bash","match":"^\\{\"command\":\"ls\",\"timeout\":5\\}$","effect":"allow"}]`
	g, _ := newGate(t, stored, nil, nil)

	d := g.Decide(context.Background(), req("Bash", `{"timeout": 5, "command": "ls"}`))
	assert.True(t, d.Allow, "canonical form sorts keys, so the order the backend used does not matter")
}

func TestRuleToolMustMatchExactly(t *testing.T) {
	g, _ := newGate(t, "", nil, nil)
	d := g.Decide(context.Background(), req("ReadFile", `{}`))
	assert.False(t, d.Allow, "Read does not cover ReadFile")
}

func TestApprovalWrittenBackCoversTheSameActionNextTime(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: true, Remember: true}}
	g, path := newGate(t, "", nil, asker)
	action := req("Bash", `{"command":"make test"}`)

	first := g.Decide(context.Background(), action)
	assert.Equal(t, "operator", first.Via)

	second := g.Decide(context.Background(), action)
	assert.True(t, second.Allow)
	assert.Equal(t, "rule", second.Via, "covered without the operator the second time")
	assert.Len(t, asker.asked(), 1)

	other := g.Decide(context.Background(), req("Bash", `{"command":"make test; rm -rf /"}`))
	assert.Equal(t, "operator", other.Via, "the write-back covers exactly that action, not a prefix of it")

	reloaded, err := LoadPolicy(path)
	require.NoError(t, err)
	_, ok := reloaded.Lookup("Bash", `{"command":"make test"}`)
	assert.True(t, ok, "the approval survives a restart")
}

func TestApprovalWithoutRememberIsNotWrittenBack(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: true}}
	g, path := newGate(t, "", nil, asker)

	g.Decide(context.Background(), req("Bash", `{"command":"x"}`))
	g.Decide(context.Background(), req("Bash", `{"command":"x"}`))
	assert.Len(t, asker.asked(), 2)
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestFailedWriteBackStillAllowsThisOnceAndSaysSo(t *testing.T) {
	asker := &fakeAsker{ans: Answer{Allow: true, Remember: true}}
	g, _ := newGate(t, "", nil, asker)
	// A path under a regular file cannot be created.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	g.Policy.path = filepath.Join(blocker, "policy.json")

	d := g.Decide(context.Background(), req("Bash", `{"command":"x"}`))
	assert.True(t, d.Allow)
	assert.Contains(t, d.Reason, "not remembered")

	g.Decide(context.Background(), req("Bash", `{"command":"x"}`))
	assert.Len(t, asker.asked(), 2, "a rule that was not persisted must not take effect")
}

func TestLoadPolicyRejectsBadRules(t *testing.T) {
	for name, stored := range map[string]string{
		"no tool":      `[{"risk":"read"}]`,
		"no risk":      `[{"tool":"X"}]`,
		"unknown risk": `[{"tool":"X","risk":"spicy"}]`,
		"bad effect":   `[{"tool":"X","effect":"maybe"}]`,
		"bad regexp":   `[{"tool":"X","risk":"read","match":"("}]`,
		"unknown key":  `[{"tool":"X","risk":"read","nope":1}]`,
		"not a list":   `{"tool":"X"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "p.json")
			require.NoError(t, os.WriteFile(path, []byte(stored), 0o600))
			_, err := LoadPolicy(path)
			assert.Error(t, err)
		})
	}
}

func TestNumbersInInputKeepTheirExactForm(t *testing.T) {
	got, err := canonical(json.RawMessage(`{"n": 12345678901234567890, "f": 1.50}`))
	require.NoError(t, err)
	assert.Equal(t, `{"f":1.50,"n":12345678901234567890}`, got)
}
