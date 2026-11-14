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
	// nodes maps an agent name to the node that holds it; an agent not listed is local.
	nodes  map[string]string
	routed []core.Message
	// to records, per delivery, the agents it was for.
	to [][]string
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
		ps = append(ps, core.Profile{Name: n, Node: f.nodes[n]})
	}
	return ps, nil
}

func (f *fakeRouter) Deliver(_ context.Context, ps []core.Profile, m core.Message) []route.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.atRoute != nil {
		f.atRoute(m)
	}
	f.routed = append(f.routed, m)
	var rs []route.Result
	var names []string
	for _, p := range ps {
		rs = append(rs, route.Result{Agent: p.Name})
		names = append(names, p.Name)
	}
	f.to = append(f.to, names)
	return rs
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

func TestNotifyReachesTheHumanButNoAgent(t *testing.T) {
	l, _, st := setup(t)
	ch := &fakeChannel{name: "c"}
	l.AddChannel(ch)

	m, err := l.Notify(context.Background(), core.Message{Sender: "pao", Text: "raw pane: enter your token"})
	require.NoError(t, err)

	require.Len(t, ch.got, 1)
	assert.Equal(t, core.MessageSystemEvent, ch.got[0].Kind)
	for _, agent := range []string{"a", "b"} {
		_, err := st.Resolve(agent, "enter your token")
		assert.ErrorIs(t, err, msgstore.ErrUnresolved, "agent %s must not be able to quote it", agent)
		_, err = st.GetScoped(agent, m.ID)
		assert.ErrorIs(t, err, msgstore.ErrNotFound, "agent %s must not be able to fetch it", agent)
	}
}

func TestNotifyOnReachesOneChannelAndAnUnknownOneStoresNothing(t *testing.T) {
	l, _, st := setup(t)
	one, two := &fakeChannel{name: "one"}, &fakeChannel{name: "two"}
	l.AddChannel(one)
	l.AddChannel(two)

	m, err := l.NotifyOn(context.Background(), "two", core.Message{Sender: "pao", Text: "a replay for the viewer"})
	require.NoError(t, err)
	assert.Empty(t, one.got, "only the channel named")
	require.Len(t, two.got, 1)
	assert.Equal(t, core.MessageSystemEvent, two.got[0].Kind)
	_, err = st.GetScoped("a", m.ID)
	assert.ErrorIs(t, err, msgstore.ErrNotFound, "no agent can fetch it")

	_, err = l.NotifyOn(context.Background(), "missing", core.Message{Sender: "pao", Text: "lost"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no channel", "refused before it was stored")
	_, err = st.Resolve("a", "lost")
	assert.ErrorIs(t, err, msgstore.ErrUnresolved)
}

type recChannel struct {
	name string
	mu   sync.Mutex
	got  []core.Message
	fail error
}

func (c *recChannel) Name() string { return c.name }
func (c *recChannel) Send(_ context.Context, m core.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, m)
	return c.fail
}
func (c *recChannel) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, m := range c.got {
		out = append(out, m.Text)
	}
	return out
}

func twoChannelLayer(t *testing.T) (*Layer, *recChannel, *recChannel) {
	t.Helper()
	store, err := msgstore.Open(filepath.Join(t.TempDir(), "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	l := New(store, nil)
	a, b := &recChannel{name: "a"}, &recChannel{name: "b"}
	l.AddChannel(a)
	l.AddChannel(b)
	return l, a, b
}

func TestABoundAgentSpeaksOnlyOnItsChannel(t *testing.T) {
	l, a, b := twoChannelLayer(t)
	require.NoError(t, l.Bind("tmp", "b"))

	_, err := l.Send(context.Background(), "tmp", core.Message{Sender: "tmp", Text: "mine"})
	require.NoError(t, err)
	_, err = l.Send(context.Background(), "other", core.Message{Sender: "other", Text: "everyone"})
	require.NoError(t, err)

	assert.Equal(t, []string{"mine", "everyone"}, b.texts())
	assert.Equal(t, []string{"everyone"}, a.texts(), "the bound agent's stream is not on the other channel; an unbound agent's still is")
}

func TestUnbindReturnsTheAgentToEveryChannel(t *testing.T) {
	l, a, b := twoChannelLayer(t)
	require.NoError(t, l.Bind("tmp", "b"))
	l.Unbind("tmp")

	_, err := l.Send(context.Background(), "tmp", core.Message{Sender: "tmp", Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, a.texts())
	assert.Equal(t, []string{"x"}, b.texts())
}

func TestBindNeedsARegisteredChannel(t *testing.T) {
	l, _, _ := twoChannelLayer(t)
	require.Error(t, l.Bind("tmp", "nope"))
	_, err := l.Send(context.Background(), "tmp", core.Message{Sender: "tmp", Text: "x"})
	require.NoError(t, err, "a failed bind leaves no binding behind")
	assert.True(t, l.HasChannel("a"))
	assert.False(t, l.HasChannel("nope"))
	assert.Equal(t, []string{"a", "b"}, l.Channels())
}

func TestBindingDoesNotNarrowPaoOwnMessages(t *testing.T) {
	l, a, b := twoChannelLayer(t)
	require.NoError(t, l.Bind("tmp", "b"))
	_, err := l.Notify(context.Background(), core.Message{Sender: "pao", Text: "alert"})
	require.NoError(t, err)
	assert.Equal(t, []string{"alert"}, a.texts())
	assert.Equal(t, []string{"alert"}, b.texts(), "an operator alert reaches every channel whoever is bound where")
}

func TestABoundChannelThatFailsIsAnErrorNotAFallback(t *testing.T) {
	l, a, b := twoChannelLayer(t)
	b.fail = errors.New("down")
	require.NoError(t, l.Bind("tmp", "b"))

	_, err := l.Send(context.Background(), "tmp", core.Message{Sender: "tmp", Text: "x"})
	require.Error(t, err)
	assert.Empty(t, a.texts(), "the stream does not spill onto another channel because its own is down")
}

func TestBindAndSendRace(t *testing.T) {
	l, _, _ := twoChannelLayer(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = l.Bind("tmp", "a"); l.Unbind("tmp") }()
		go func() {
			defer wg.Done()
			_, _ = l.Send(context.Background(), "tmp", core.Message{Sender: "tmp", Text: "x"})
		}()
	}
	wg.Wait()
}

func TestAllowNarrowsWhoIsAddressedAndWhoTheMessageIsStoredFor(t *testing.T) {
	store, err := msgstore.Open(filepath.Join(t.TempDir(), "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	r := &fakeRouter{agents: map[string][]string{"team": {"a", "b"}}}
	l := New(store, r)

	d, err := l.Receive(context.Background(), Inbound{
		Sender: "h", Text: "hi", Target: core.Target{Kind: core.ByTag, Value: "team"},
		Allow: func(p core.Profile) bool { return p.Name == "b" },
	})
	require.NoError(t, err)
	require.Len(t, d.Results, 1)
	assert.Equal(t, "b", d.Results[0].Agent, "only the allowed agent is delivered to")
	_, err = l.Fetch("b", d.Message.ID)
	assert.NoError(t, err)
	_, err = l.Fetch("a", d.Message.ID)
	assert.Error(t, err, "and the message is in no scope but theirs")
}

func TestAllowRefusingEveryoneIsTheSameErrorAsNoMatch(t *testing.T) {
	store, err := msgstore.Open(filepath.Join(t.TempDir(), "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	l := New(store, &fakeRouter{agents: map[string][]string{"a": {"a"}}})

	_, err = l.Receive(context.Background(), Inbound{
		Sender: "h", Text: "hi", Target: core.Target{Kind: core.ByName, Value: "a"},
		Allow: func(core.Profile) bool { return false },
	})
	require.ErrorIs(t, err, core.ErrNoMatch)
	assert.Equal(t, route.NoMatch(core.Target{Kind: core.ByName, Value: "a"}).Error(), err.Error(), "word for word what the router says for an agent that does not exist")
}

// meshLayer is a layer on node "n-a" in a mesh of n-a, n-b and n-c, with the local
// agent "a" and the agents "far" on n-b and "farther" on n-c.
func meshLayer(t *testing.T) (*Layer, *fakeRouter, *msgstore.Store) {
	t.Helper()
	l, r, st := setup(t)
	r.agents["far"], r.agents["farther"], r.agents["both"] = []string{"far"}, []string{"farther"}, []string{"a", "far"}
	r.nodes = map[string]string{"far": "n-b", "farther": "n-c"}
	l.SetNode("n-a", func(n string) bool { return n == "n-b" || n == "n-c" })
	return l, r, st
}

func TestAReplyToAnAgentOnAnotherNodeIsForwardedAsThatNodesOwnId(t *testing.T) {
	l, r, _ := meshLayer(t)
	d, err := l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "yes", Reply: "n-b:m7", Target: core.Target{Kind: core.ByName, Value: "far"}})
	require.NoError(t, err)
	require.Len(t, r.routed, 1)
	assert.Equal(t, "m7", r.routed[0].Reply, "the node is given its own id for the message")
	assert.Empty(t, r.routed[0].ReplyQuote)
	assert.Equal(t, "n-b:m7", mustGet(t, l, d.Message.ID).Reply, "what is stored here keeps what the human named")
}

func mustGet(t *testing.T, l *Layer, id string) core.Message {
	t.Helper()
	m, err := l.store.Get(id)
	require.NoError(t, err)
	return m
}

func TestAReplyThatDoesNotFitTheAgentsNodeIsNotFoundAndNothingIsDelivered(t *testing.T) {
	l, r, _ := meshLayer(t)
	for name, c := range map[string]struct{ reply, target string }{
		"bare id":              {"m7", "far"},
		"another node's id":    {"n-c:m7", "far"},
		"an unknown node":      {"n-z:m7", "far"},
		"this node's id":       {"n-a:m7", "far"},
		"a remote id to local": {"n-b:m7", "a"},
	} {
		_, err := l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "yes", Reply: c.reply, Target: core.Target{Kind: core.ByName, Value: c.target}})
		require.Error(t, err, name)
		assert.ErrorIs(t, err, msgstore.ErrNotFound, name)
	}
	assert.Empty(t, r.routed, "nothing was delivered on a reply that does not resolve")
}

func TestAQuotedReplyToAnAgentOnAnotherNodeIsForwardedForItsNodeToResolve(t *testing.T) {
	l, r, _ := meshLayer(t)
	d, err := l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "yes", ReplyQuote: "the plan", Target: core.Target{Kind: core.ByName, Value: "far"}})
	require.NoError(t, err, "not resolved here: the agent's scope is on its node")
	require.Len(t, r.routed, 1)
	assert.Equal(t, "the plan", r.routed[0].ReplyQuote)
	assert.Empty(t, r.routed[0].Reply)
	assert.Empty(t, mustGet(t, l, d.Message.ID).ReplyQuote, "the quote is never stored")

	_, err = l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "yes", ReplyQuote: "the plan", Target: core.Target{Kind: core.ByName, Value: "both"}})
	require.Error(t, err, "a quote to several agents is refused, across nodes as on one")
}

func TestAReplyToAFanOutAcrossNodesNeedsTheMessageInEveryAgentsScope(t *testing.T) {
	l, r, st := meshLayer(t)
	m, err := st.Add(core.Message{Sender: "a", Text: "mine"}, "a")
	require.NoError(t, err)
	_, err = l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "re", Reply: m.ID, Target: core.Target{Kind: core.ByName, Value: "both"}})
	require.Error(t, err, "a local id cannot be in the scope of the agent on another node")
	assert.ErrorIs(t, err, msgstore.ErrNotFound)
	assert.Empty(t, r.routed)
}

func TestAnIdIsQualifiedByItsNodeInAMeshAndAcceptedEitherWayForItsOwn(t *testing.T) {
	l, r, st := meshLayer(t)
	assert.Equal(t, "n-a:m5", l.Qualify("m5"))
	assert.Empty(t, l.Qualify(""), "no id stays no id")
	plain, _, _ := setup(t)
	assert.Equal(t, "m5", plain.Qualify("m5"), "outside a mesh an id is as it was")

	m, err := st.Add(core.Message{Sender: "a", Text: "mine"}, "a")
	require.NoError(t, err)
	_, err = l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "re", Reply: "n-b:" + m.ID, Target: core.Target{Kind: core.ByName, Value: "a"}})
	assert.ErrorIs(t, err, msgstore.ErrNotFound, "another node's id is not this agent's message, even when the number matches one that is")
	for _, ref := range []string{m.ID, "n-a:" + m.ID} {
		d, err := l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "re", Reply: ref, Target: core.Target{Kind: core.ByName, Value: "a"}})
		require.NoError(t, err, ref)
		assert.Equal(t, m.ID, mustGet(t, l, d.Message.ID).Reply, "stored as its own id, whichever way it was named")
	}
	require.Len(t, r.routed, 2)

	require.NoError(t, l.React("n-a:"+m.ID, "human", "👍"), "a reaction by the id the human saw")
	assert.ErrorIs(t, l.React("n-b:"+m.ID, "human", "👍"), msgstore.ErrNotFound, "another node's id is not a message here")
}

func TestOutsideAMeshAnIdWithAColonIsNotAMessage(t *testing.T) {
	l, _, st := setup(t)
	m, err := st.Add(core.Message{Sender: "a", Text: "mine"}, "a")
	require.NoError(t, err)
	_, err = l.Receive(context.Background(), Inbound{Source: "x", Sender: "human", Text: "re", Reply: "n-a:" + m.ID, Target: core.Target{Kind: core.ByName, Value: "a"}})
	assert.ErrorIs(t, err, msgstore.ErrNotFound)
}
