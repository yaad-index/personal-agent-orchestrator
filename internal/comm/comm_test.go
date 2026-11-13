package comm

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// fakeRouter resolves a target by name to the agents it knows and records what
// was routed.
type fakeRouter struct {
	mu     sync.Mutex
	agents map[string][]string // tag or name -> agent names
	routed []core.Message
	// seenInStore, when set, is called at Route time to check the message was
	// already stored.
	atRoute func(core.Message)
}

func (f *fakeRouter) Resolve(t core.Target) ([]core.Profile, error) {
	names, ok := f.agents[t.Value]
	if !ok {
		return nil, core.ErrNoMatch
	}
	var ps []core.Profile
	for _, n := range names {
		ps = append(ps, core.Profile{Name: n})
	}
	return ps, nil
}

func (f *fakeRouter) Route(_ context.Context, t core.Target, m core.Message) ([]route.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.atRoute != nil {
		f.atRoute(m)
	}
	f.routed = append(f.routed, m)
	var rs []route.Result
	for _, n := range f.agents[t.Value] {
		rs = append(rs, route.Result{Agent: n})
	}
	return rs, nil
}

type fakeChannel struct {
	name string
	got  []core.Message
	err  error
}

func (c *fakeChannel) Name() string { return c.name }
func (c *fakeChannel) Send(_ context.Context, m core.Message) error {
	c.got = append(c.got, m)
	return c.err
}

func setup(t *testing.T) (*Layer, *fakeRouter, *msgstore.Store) {
	t.Helper()
	st, err := msgstore.Open(filepath.Join(t.TempDir(), "m.db"), 50)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	r := &fakeRouter{agents: map[string][]string{"a": {"a"}, "team": {"a", "b"}}}
	return New(st, r), r, st
}

func byName(n string) core.Target { return core.Target{Kind: core.ByName, Value: n} }

func TestReceiveNormalizesStoresAndRoutes(t *testing.T) {
	l, r, st := setup(t)

	d, err := l.Receive(context.Background(), Inbound{Source: "chat", Sender: "h", Text: "hi", Target: byName("a")})
	require.NoError(t, err)

	assert.NotEmpty(t, d.Message.ID)
	assert.Equal(t, core.MessageContent, d.Message.Kind)
	require.Len(t, r.routed, 1)
	assert.Equal(t, d.Message, r.routed[0])
	assert.Equal(t, "[h]: hi", r.routed[0].Line(), "the agent reads only sender and text")

	got, err := st.GetScoped("a", d.Message.ID)
	require.NoError(t, err)
	assert.Equal(t, "hi", got.Text)
}

func TestTheSourceKindNeverReachesTheAgent(t *testing.T) {
	l, r, _ := setup(t)

	_, err := l.Receive(context.Background(), Inbound{Source: "secret-source-kind", Sender: "h", Text: "hi", Target: byName("a")})
	require.NoError(t, err)

	m := r.routed[0]
	assert.NotContains(t, m.Line(), "secret-source-kind")
	assert.NotContains(t, m.Sender, "secret-source-kind")
	assert.NotContains(t, m.Text, "secret-source-kind")
}

func TestStoredBeforeItIsRouted(t *testing.T) {
	l, r, st := setup(t)
	var foundAtRoute bool
	r.atRoute = func(m core.Message) {
		_, err := st.GetScoped("a", m.ID)
		foundAtRoute = err == nil
	}

	_, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	assert.True(t, foundAtRoute, "a fast reply quoting the message must already find it")
}

func TestFanOutIsStoredForEveryAddressedAgent(t *testing.T) {
	l, _, st := setup(t)

	d, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "all hands", Target: core.Target{Kind: core.ByTag, Value: "team"}})
	require.NoError(t, err)
	for _, agent := range []string{"a", "b"} {
		_, err := st.GetScoped(agent, d.Message.ID)
		assert.NoError(t, err, agent)
	}
}

func TestUnmatchedTargetStoresNothing(t *testing.T) {
	l, _, st := setup(t)

	_, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "lost words", Target: byName("nobody")})
	assert.ErrorIs(t, err, core.ErrNoMatch)

	_, err = st.Resolve("a", "lost words")
	assert.ErrorIs(t, err, msgstore.ErrUnresolved)
}

type upper struct{}

func (upper) Preprocess(_ context.Context, in Inbound) (Inbound, error) {
	in.Text = "voice: " + in.Text
	return in, nil
}

type failing struct{}

func (failing) Preprocess(context.Context, Inbound) (Inbound, error) {
	return Inbound{}, errors.New("cannot transcribe")
}

func TestPreprocessorRunsOnceForItsSourceOnly(t *testing.T) {
	l, r, _ := setup(t)
	l.Preprocess("voice", upper{})

	_, err := l.Receive(context.Background(), Inbound{Source: "voice", Sender: "h", Text: "hello", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.Receive(context.Background(), Inbound{Source: "chat", Sender: "h", Text: "hello", Target: byName("a")})
	require.NoError(t, err)

	assert.Equal(t, "voice: hello", r.routed[0].Text)
	assert.Equal(t, "hello", r.routed[1].Text, "no per-source special case for other sources")
}

func TestPreprocessFailureStopsTheMessage(t *testing.T) {
	l, r, _ := setup(t)
	l.Preprocess("voice", failing{})

	_, err := l.Receive(context.Background(), Inbound{Source: "voice", Sender: "h", Text: "x", Target: byName("a")})
	assert.ErrorContains(t, err, `preprocess "voice"`)
	assert.ErrorContains(t, err, "cannot transcribe")
	assert.Empty(t, r.routed)
}

func TestReplyByID(t *testing.T) {
	l, _, _ := setup(t)
	first, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "question?"})
	require.NoError(t, err)

	d, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "yes", Reply: first.ID, Target: byName("a")})
	require.NoError(t, err)
	assert.Equal(t, first.ID, d.Message.Reply)

	_, err = l.Receive(context.Background(), Inbound{Sender: "h", Text: "yes", Reply: "m9999", Target: byName("a")})
	assert.ErrorIs(t, err, msgstore.ErrNotFound)
}

func TestReplyByIDCannotReachAnotherAgentsMessage(t *testing.T) {
	l, _, _ := setup(t)
	other, err := l.Send(context.Background(), "b", core.Message{Sender: "b", Text: "b's private output"})
	require.NoError(t, err)

	_, err = l.Receive(context.Background(), Inbound{Sender: "h", Text: "re", Reply: other.ID, Target: byName("a")})
	assert.ErrorIs(t, err, msgstore.ErrNotFound)
}

func TestReplyByQuoteFallback(t *testing.T) {
	l, _, _ := setup(t)
	first, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "shall I deploy tonight?"})
	require.NoError(t, err)

	d, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "yes", ReplyQuote: "deploy tonight", Target: byName("a")})
	require.NoError(t, err)
	assert.Equal(t, first.ID, d.Message.Reply)

	_, err = l.Receive(context.Background(), Inbound{Sender: "h", Text: "yes", ReplyQuote: "never said this", Target: byName("a")})
	assert.ErrorIs(t, err, msgstore.ErrUnresolved)
}

func TestQuotedReplyToAFanOutIsRefused(t *testing.T) {
	l, _, _ := setup(t)
	_, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "yes", ReplyQuote: "x", Target: core.Target{Kind: core.ByTag, Value: "team"}})
	assert.ErrorContains(t, err, "exactly one addressed agent")
}

func TestSendStoresInTheAgentsScopeAndReachesEveryChannel(t *testing.T) {
	l, _, st := setup(t)
	one, two := &fakeChannel{name: "one"}, &fakeChannel{name: "two"}
	l.AddChannel(one)
	l.AddChannel(two)

	m, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "result ready", Kind: core.MessageSystemEvent})
	require.NoError(t, err)

	assert.Equal(t, []core.Message{m}, one.got)
	assert.Equal(t, []core.Message{m}, two.got)
	assert.Equal(t, core.MessageSystemEvent, m.Kind)

	got, err := st.Resolve("a", "result ready")
	require.NoError(t, err, "the agent can quote its own output")
	assert.Equal(t, m.ID, got.ID)
	_, err = st.Resolve("b", "result ready")
	assert.ErrorIs(t, err, msgstore.ErrUnresolved)
}

func TestSendChannelFailureDoesNotStopOthers(t *testing.T) {
	l, _, _ := setup(t)
	bad, good := &fakeChannel{name: "bad", err: errors.New("down")}, &fakeChannel{name: "good"}
	l.AddChannel(bad)
	l.AddChannel(good)

	_, err := l.Send(context.Background(), "a", core.Message{Text: "x"})
	assert.ErrorContains(t, err, `channel "bad"`)
	assert.Len(t, good.got, 1)
}

func TestAgentToolsAreScoped(t *testing.T) {
	l, _, _ := setup(t)
	mine, err := l.Send(context.Background(), "a", core.Message{Text: "alpha note"})
	require.NoError(t, err)
	_, err = l.Send(context.Background(), "b", core.Message{Text: "beta note"})
	require.NoError(t, err)

	got, err := l.Fetch("a", mine.ID)
	require.NoError(t, err)
	assert.Equal(t, "alpha note", got.Text)

	q, err := l.ResolveQuote("a", "alpha")
	require.NoError(t, err)
	assert.Equal(t, mine.ID, q.ID)

	_, err = l.ResolveQuote("a", "beta")
	assert.ErrorIs(t, err, msgstore.ErrUnresolved)

	require.NoError(t, l.React(mine.ID, "h", "👍"))
	got, _ = l.Fetch("a", mine.ID)
	assert.Equal(t, []core.Reaction{{By: "h", Emoji: "👍"}}, got.Reactions)
}

func TestReplyByIDToAFanOutNeedsTheMessageInEveryAddressedAgentsScope(t *testing.T) {
	l, _, _ := setup(t)
	team := core.Target{Kind: core.ByTag, Value: "team"} // addresses a and b

	shared, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "all hands", Target: team})
	require.NoError(t, err)
	d, err := l.Receive(context.Background(), Inbound{Sender: "h", Text: "re all", Reply: shared.Message.ID, Target: team})
	require.NoError(t, err, "a message both agents were shown can be replied to")
	assert.Equal(t, shared.Message.ID, d.Message.Reply)

	onlyA, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "a's private output"})
	require.NoError(t, err)
	_, err = l.Receive(context.Background(), Inbound{Sender: "h", Text: "re", Reply: onlyA.ID, Target: team})
	assert.ErrorIs(t, err, msgstore.ErrNotFound, "b was never shown it, so the fan-out cannot reference it")

	_, err = l.Receive(context.Background(), Inbound{Sender: "h", Text: "re", Reply: "m9999", Target: team})
	assert.ErrorIs(t, err, msgstore.ErrNotFound, "absent looks the same as out of scope")
}

func TestAgentReactionIsScoped(t *testing.T) {
	l, _, _ := setup(t)
	mine, err := l.Send(context.Background(), "a", core.Message{Text: "mine"})
	require.NoError(t, err)
	theirs, err := l.Send(context.Background(), "b", core.Message{Text: "theirs"})
	require.NoError(t, err)

	require.NoError(t, l.ReactAs("a", mine.ID, "👍"))
	assert.ErrorIs(t, l.ReactAs("a", theirs.ID, "👍"), msgstore.ErrNotFound)

	require.NoError(t, l.React(theirs.ID, "human", "👍"), "the human may react to any message")
}
