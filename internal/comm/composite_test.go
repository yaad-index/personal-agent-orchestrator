package comm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestACompositeSplitsOnItsFirstDashAndTheNativeIdStaysOpaque(t *testing.T) {
	for _, c := range []struct {
		in     string
		id     int
		native string
	}{
		{"1-42", 1, "42"},
		{"12-a-b-c", 12, "a-b-c"},
		{"3-x:y-z", 3, "x:y-z"},
		{"7--lead", 7, "-lead"},
	} {
		id, native, ok := ParseComposite(c.in)
		require.True(t, ok, c.in)
		assert.Equal(t, c.id, id, c.in)
		assert.Equal(t, c.native, native, c.in)
		assert.Equal(t, c.in, FormatComposite(id, native), "it round-trips")
	}
}

func TestWhatIsNotACompositeIsRefused(t *testing.T) {
	for _, s := range []string{"", "1", "1-", "-5", "0-5", "01-5", "a-5", "1a-5", "+1-5", " 1-5", "99999999999999999999-5"} {
		_, _, ok := ParseComposite(s)
		assert.False(t, ok, "%q", s)
	}
}

func compSetup(t *testing.T) (*Layer, *fakeRouter, *fakeIdentity) {
	t.Helper()
	l, r, id, _ := chanSetup(t)
	l.SetChannelIDs(map[string]int{"chat": 1, "other": 2})
	return l, r, id
}

func TestAnInboundIsFoundAgainByItsCompositeAndTheAdapterRecoversTheNativeId(t *testing.T) {
	l, _, _ := compSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a"), Native: "m-9:x"})
	require.NoError(t, err)
	id := res.Delivery.Message.ID

	got, ok := l.ResolveComposite("1-m-9:x")
	require.True(t, ok)
	assert.Equal(t, id, got)
	comp, ok := l.CompositeOf(id)
	require.True(t, ok)
	assert.Equal(t, "1-m-9:x", comp)

	native, ok := l.NativeOf("chat", comp)
	require.True(t, ok)
	assert.Equal(t, "m-9:x", native)
}

func TestAChannelActsOnlyOnItsOwnComposites(t *testing.T) {
	l, _, _ := compSetup(t)
	_, ok := l.NativeOf("other", "1-42")
	assert.False(t, ok, "channel 2 is not handed channel 1's message")
	_, ok = l.NativeOf("missing", "1-42")
	assert.False(t, ok)
	native, ok := l.NativeOf("chat", "1-42")
	require.True(t, ok)
	assert.Equal(t, "42", native)
}

func TestTheSameNativeIdOnTwoChannelsIsTwoMessages(t *testing.T) {
	l, _, id := compSetup(t)
	id.bound["other\x00op"] = "alice"
	l.SetScopes(map[string]Scope{"chat": {Owner: "alice"}, "other": {Owner: "alice"}})
	l.SetDefaults(mapDefaults{"chat": "a", "other": "a"})
	a, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "a", Target: byName("a"), Native: "5"})
	require.NoError(t, err)
	b, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "other", Sender: "op", Text: "b", Target: byName("a"), Native: "5"})
	require.NoError(t, err)

	ga, _ := l.ResolveComposite("1-5")
	gb, _ := l.ResolveComposite("2-5")
	assert.Equal(t, a.Delivery.Message.ID, ga)
	assert.Equal(t, b.Delivery.Message.ID, gb)
	assert.NotEqual(t, ga, gb)
}

func TestAnUnresolvedCompositeGetsOneAnswerWhateverTheReason(t *testing.T) {
	l, _, _ := compSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a"), Native: "5"})
	require.NoError(t, err)
	for _, s := range []string{"1-6", "9-5", "garbage", "", "1-", "other:1-5"} {
		got, ok := l.ResolveComposite(s)
		assert.False(t, ok, s)
		assert.Empty(t, got, s)
	}
}

func TestTheCompositeWindowDropsTheOldestPair(t *testing.T) {
	l, _, _ := compSetup(t)
	l.SetCompositeWindow(2)
	var ids []string
	for i := 1; i <= 3; i++ {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "x", Target: byName("a"), Native: fmt.Sprint(i)})
		require.NoError(t, err)
		ids = append(ids, res.Delivery.Message.ID)
	}
	_, ok := l.ResolveComposite("1-1")
	assert.False(t, ok, "the oldest fell out")
	_, ok = l.CompositeOf(ids[0])
	assert.False(t, ok, "in both directions")
	for i := 2; i <= 3; i++ {
		_, ok = l.ResolveComposite(FormatComposite(1, fmt.Sprint(i)))
		assert.True(t, ok)
	}
	l.SetCompositeWindow(0)
	_, ok = l.ResolveComposite("1-3")
	assert.True(t, ok, "a window below one changes nothing")
}

func TestInAMeshTheNodeGoesInFrontAndAnotherNodesCompositeIsNotHeldHere(t *testing.T) {
	l, _, _ := compSetup(t)
	l.SetNode("n1", func(s string) bool { return s == "n2" })
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a"), Native: "7"})
	require.NoError(t, err)

	comp, ok := l.CompositeOf(res.Delivery.Message.ID)
	require.True(t, ok)
	assert.Equal(t, "n1:1-7", comp)
	got, ok := l.ResolveComposite("n1:1-7")
	assert.True(t, ok)
	assert.Equal(t, res.Delivery.Message.ID, got)
	got, ok = l.ResolveComposite("1-7")
	assert.True(t, ok, "unqualified is this node's")
	assert.Equal(t, res.Delivery.Message.ID, got)
	_, ok = l.ResolveComposite("n2:1-7")
	assert.False(t, ok, "another node's is not held here")
	native, ok := l.NativeOf("chat", "n1:1-7")
	assert.True(t, ok)
	assert.Equal(t, "7", native)
	_, ok = l.NativeOf("chat", "n2:1-7")
	assert.False(t, ok)
}

type nativeChan struct {
	fakeChannel
	native string
	nerr   error
}

func (c *nativeChan) SendNative(_ context.Context, m core.Message) ([]string, error) {
	c.got = append(c.got, m)
	if c.native == "" {
		return nil, c.nerr
	}
	return []string{c.native}, c.nerr
}

func TestAMessagePaoSendsIsNamedByTheIdThePlatformGaveIt(t *testing.T) {
	l, _, _ := compSetup(t)
	nc := &nativeChan{fakeChannel: fakeChannel{name: "chat"}, native: "out-1"}
	l.AddChannel(nc)
	stored, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "answer"})
	require.NoError(t, err)

	got, ok := l.ResolveComposite("1-out-1")
	require.True(t, ok)
	assert.Equal(t, stored.ID, got)
	comp, ok := l.CompositeOf(stored.ID)
	require.True(t, ok)
	assert.Equal(t, "1-out-1", comp)

	_, err = l.notifyOn(context.Background(), "chat", core.Message{Text: "alert"})
	require.NoError(t, err)
	assert.Len(t, nc.got, 2, "the targeted path goes through the same delivery")
}

func TestAChannelThatGivesNoNativeIdRecordsNothingAndOneThatFailsNamesOnlyWhatWentOut(t *testing.T) {
	l, _, _ := compSetup(t)
	nc := &nativeChan{fakeChannel: fakeChannel{name: "chat"}}
	l.AddChannel(nc)
	stored, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "x"})
	require.NoError(t, err)
	_, ok := l.CompositeOf(stored.ID)
	assert.False(t, ok, "no native id, no composite")

	nc.nerr = errors.New("down")
	stored, err = l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "y"})
	require.Error(t, err)
	_, ok = l.CompositeOf(stored.ID)
	assert.False(t, ok, "a send that reached nobody names nothing")

	nc.native = "9"
	stored, err = l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "z"})
	require.Error(t, err)
	comp, ok := l.CompositeOf(stored.ID)
	require.True(t, ok, "a copy that did go out is still named, whatever failed after it")
	assert.Equal(t, "1-9", comp)
}

func TestARepliedToNativeIdResolvesToThePaoMessage(t *testing.T) {
	l, _, _ := compSetup(t)
	nc := &nativeChan{fakeChannel: fakeChannel{name: "chat"}, native: "out-1"}
	l.AddChannel(nc)
	q, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "question?"})
	require.NoError(t, err)

	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Target: byName("a"), ReplyNative: "out-1"})
	require.NoError(t, err)
	assert.Equal(t, q.ID, res.Delivery.Message.Reply)

	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Target: byName("a"), ReplyNative: "gone", ReplyQuote: "question?"})
	require.NoError(t, err)
	assert.Equal(t, q.ID, res.Delivery.Message.Reply, "an unresolved native id falls back to the quote")

	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Target: byName("a"), Reply: q.ID, ReplyNative: "gone"})
	require.NoError(t, err)
	assert.Equal(t, q.ID, res.Delivery.Message.Reply, "an explicit pao id wins")
}

func TestAColonInTheNativeIdIsNotANodeQualifier(t *testing.T) {
	l, _, _ := compSetup(t)
	l.SetNode("n1", func(s string) bool { return s == "n2" })
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a"), Native: "m-9:x"})
	require.NoError(t, err)
	for _, s := range []string{"1-m-9:x", "n1:1-m-9:x"} {
		got, ok := l.ResolveComposite(s)
		require.True(t, ok, s)
		assert.Equal(t, res.Delivery.Message.ID, got, s)
		native, ok := l.NativeOf("chat", s)
		require.True(t, ok, s)
		assert.Equal(t, "m-9:x", native, s)
	}
	_, ok := l.ResolveComposite("n2:1-m-9:x")
	assert.False(t, ok)
}

func TestOneMessageSentToTwoChannelsIsNamedOnEachAndBothResolveToIt(t *testing.T) {
	l, _, _ := compSetup(t)
	l.AddChannel(&nativeChan{fakeChannel: fakeChannel{name: "chat"}, native: "c-1"})
	l.AddChannel(&nativeChan{fakeChannel: fakeChannel{name: "other"}, native: "o-1"})
	stored, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "x"})
	require.NoError(t, err)

	for _, s := range []string{"1-c-1", "2-o-1"} {
		got, ok := l.ResolveComposite(s)
		require.True(t, ok, s)
		assert.Equal(t, stored.ID, got, s)
	}
	comp, ok := l.CompositeOf(stored.ID)
	require.True(t, ok)
	assert.Equal(t, "2-o-1", comp, "the id's own composite is the latest")
}

func TestAnEvictedCompositeDoesNotTakeAnotherOfTheSameMessageWithIt(t *testing.T) {
	l, _, _ := compSetup(t)
	l.SetCompositeWindow(3)
	l.AddChannel(&nativeChan{fakeChannel: fakeChannel{name: "chat"}, native: "c-1"})
	l.AddChannel(&nativeChan{fakeChannel: fakeChannel{name: "other"}, native: "o-1"})
	stored, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "x"})
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "y", Target: byName("a"), Native: fmt.Sprint("n", i)})
		require.NoError(t, err)
	}
	_, ok := l.ResolveComposite("1-c-1")
	assert.False(t, ok, "the oldest composite fell out")
	got, ok := l.ResolveComposite("2-o-1")
	require.True(t, ok)
	assert.Equal(t, stored.ID, got)
	comp, ok := l.CompositeOf(stored.ID)
	require.True(t, ok, "and the message still has its own composite")
	assert.Equal(t, "2-o-1", comp)
}

func TestANativeIdSeenTwiceTakesOneSlotOfTheWindow(t *testing.T) {
	l, _, _ := compSetup(t)
	l.SetCompositeWindow(2)
	for _, n := range []string{"1", "1", "2"} {
		_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "x", Target: byName("a"), Native: n})
		require.NoError(t, err)
	}
	for _, n := range []string{"1", "2"} {
		_, ok := l.ResolveComposite("1-" + n)
		assert.True(t, ok, "both still fit in a window of two: %s", n)
	}
}
