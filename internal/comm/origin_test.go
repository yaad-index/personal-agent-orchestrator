package comm

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// convChannel is a channel with conversations, like a bot: it asks the layer
// where each message goes when it is handed one, and records it per conversation.
type convChannel struct {
	name string
	l    *Layer
	mu   sync.Mutex
	got  map[string][]string // conversation -> texts
}

func (c *convChannel) Name() string { return c.name }
func (c *convChannel) Send(_ context.Context, m core.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, to := range c.l.Destinations(c.name, m) {
		text := m.Text
		if m.Label != "" {
			text = m.Label + " " + text
		}
		c.got[to] = append(c.got[to], text)
	}
	return nil
}

func (c *convChannel) take() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	got := c.got
	c.got = map[string][]string{}
	return got
}

// originSetup is a layer with the channel "chat", where op is the operator and the
// group -5 is admitted, and the local channel.
func originSetup(t *testing.T) (*Layer, *fakeRouter, *convChannel, *recChannel) {
	t.Helper()
	l, r, _, _ := groupSetup(t)
	chat := &convChannel{name: "chat", l: l, got: map[string][]string{}}
	local := &recChannel{name: "local"}
	l.AddChannel(chat)
	l.AddChannel(local)
	return l, r, chat, local
}

func fromChat(t *testing.T, l *Layer, in ChannelInbound) {
	t.Helper()
	res, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
}

func say(t *testing.T, l *Layer, text string) {
	t.Helper()
	_, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Kind: core.MessageContent, Text: text})
	require.NoError(t, err)
}

func privately(text string) ChannelInbound {
	return ChannelInbound{Channel: "chat", Sender: "op", Text: text, Target: byName("a")}
}

func TestAPrivateMessageIsAnsweredPrivatelyEvenWithAnAdmittedGroup(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, privately("hi"))
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"op": {"hello"}}, chat.take(), "the operator's private conversation, not the group")
	assert.Empty(t, local.texts(), "nor another channel")
}

func TestAGroupMessageIsAnsweredInTheGroup(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, inGroup("-5", "op", "hi"))
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"-5": {"hello"}}, chat.take(), "the group, not the operator privately")
	assert.Empty(t, local.texts())
}

func TestTheNextDeliveredMessageMovesTheOrigin(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	fromChat(t, l, privately("one"))
	say(t, l, "a")
	say(t, l, "b")
	assert.Equal(t, map[string][]string{"op": {"a", "b"}}, chat.take(), "all output of the turn")
	fromChat(t, l, inGroup("-5", "op", "two"))
	say(t, l, "c")
	assert.Equal(t, map[string][]string{"-5": {"c"}}, chat.take())
}

func TestAMessageFromTheLocalChannelIsAnsweredThere(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	say(t, l, "hello")
	assert.Equal(t, []string{"hello"}, local.texts())
	assert.Empty(t, chat.take(), "the chat channel is not handed it")
}

// Until standing bindings are configured, output with no origin goes where an
// agent's output went before: every channel, each showing it as before.
func TestOutputWithNoOriginGoesWhereItWentBefore(t *testing.T) {
	l, _, chat, local := originSetup(t)
	say(t, l, "before any message")
	assert.Equal(t, map[string][]string{"-5": {"before any message"}}, chat.take())
	assert.Equal(t, []string{"before any message"}, local.texts())
}

func TestAMessageFromASourceThatIsNotAChannelStartsATurnWithNoOrigin(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, privately("hi"))
	_, err := l.Receive(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "from an agent", Target: byName("a")})
	require.NoError(t, err)
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"-5": {"hello"}}, chat.take(), "not the earlier private origin")
	assert.Equal(t, []string{"hello"}, local.texts())
}

func TestADeliveryThatFailedDoesNotMoveTheOrigin(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	fromChat(t, l, privately("hi"))
	r.fail = map[string]error{"a": errors.New("down")}
	fromChat(t, l, inGroup("-5", "op", "lost"))
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"op": {"hello"}}, chat.take())
}

func TestEachAgentAMessageReachesHasItsOrigin(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	fromChat(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: core.Target{Kind: core.ByTag, Value: "team"}})
	fromChat(t, l, inGroup("-5", "op", "only a"))
	_, err := l.Send(context.Background(), "b", core.Message{Sender: "b", Kind: core.MessageContent, Text: "from b"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"from b"}}, chat.take(), "b's turn began in private; a's later message does not move it")
}

// A reply whose origin fails the reader rule is held back, never sent somewhere
// else (ADR-0029 point 6).
func TestAReplyTheOriginMayNotSeeIsHeldBackNotRedirected(t *testing.T) {
	for name, in := range map[string]ChannelInbound{"private": privately("hi"), "group": inGroup("-5", "op", "hi")} {
		l, r, chat, local := originSetup(t)
		r.agents["b"] = []string{"b"}
		fromChat(t, l, in)
		l.SetOperatorAccess(func(p core.Profile) bool { return p.Name != "a" })
		say(t, l, "secret")
		assert.Empty(t, chat.take(), name)
		assert.Empty(t, local.texts(), name)
	}
}

func TestAReplyToAGroupNoLongerAdmittedIsHeldBack(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	fromChat(t, l, inGroup("-5", "op", "hi"))
	l.SetGroups(map[string]map[string]Group{"chat": {"-7": {Name: "other", Policy: PolicyOperators}}})
	say(t, l, "hello")
	assert.Empty(t, chat.take(), "not the group, and not the remaining group either")
}

func TestAReplyToAnOperatorNoLongerFlaggedIsHeldBack(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	id := l.identity.(*fakeIdentity)
	fromChat(t, l, privately("hi"))
	id.operators["alice"] = false
	say(t, l, "hello")
	assert.Empty(t, chat.take())
}

// An agent whose output is bound to a channel keeps that binding: the channel
// shows all its output as before, and the origin gets the reply as well.
func TestABoundChannelStillGetsTheOutputAndTheOriginToo(t *testing.T) {
	l, _, chat, local := originSetup(t)
	require.NoError(t, l.Bind("a", "local"))
	fromChat(t, l, privately("hi"))
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"op": {"hello"}}, chat.take())
	assert.Equal(t, []string{"hello"}, local.texts())
}

func TestAnOriginOnTheBoundChannelGetsOneCopy(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	require.NoError(t, l.Bind("a", "chat"))
	fromChat(t, l, inGroup("-5", "op", "hi"))
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"-5": {"hello"}}, chat.take(), "the group shows the bound output and is the origin: once")

	fromChat(t, l, privately("again"))
	say(t, l, "there")
	assert.Equal(t, map[string][]string{"-5": {"there"}, "op": {"there"}}, chat.take(), "the bound channel's group, and the private origin")
}

func TestPaosOwnMessagesAreNotRoutedByAnOrigin(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, privately("hi"))
	_, err := l.Notify(context.Background(), core.Message{Sender: "pao", Text: "allow?", Actions: question.Actions})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"allow?"}}, chat.take(), "the operator privately, as with no origin")
	assert.Len(t, local.texts(), 1)
}

func TestAMessageForwardedToAnotherNodeNotesTheOriginToo(t *testing.T) {
	l, _, _ := meshLayer(t)
	local, other := &recChannel{name: "local"}, &recChannel{name: "other"}
	l.AddChannel(local)
	l.AddChannel(other)
	_, err := l.Receive(context.Background(), Inbound{Source: "local", Sender: "human", Text: "yes", ReplyQuote: "the plan", Target: byName("far")})
	require.NoError(t, err)
	_, err = l.Send(context.Background(), "far", core.Message{Sender: "far", Kind: core.MessageContent, Text: "ok"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, local.texts())
	assert.Empty(t, other.texts())
}

// An agent may answer as soon as it reads a message, before delivery returns: the
// answer already goes to the conversation the message came from.
func TestAnAnswerWrittenDuringDeliveryGoesToTheNewOrigin(t *testing.T) {
	for name, c := range map[string]struct {
		before func(*testing.T, *Layer)
		in     ChannelInbound
		want   map[string][]string
	}{
		"from a group to private": {func(t *testing.T, l *Layer) { fromChat(t, l, inGroup("-5", "op", "first")) }, privately("now"), map[string][]string{"op": {"quick"}}},
		"from private to a group": {func(t *testing.T, l *Layer) { fromChat(t, l, privately("first")) }, inGroup("-5", "op", "now"), map[string][]string{"-5": {"quick"}}},
		"the first message":       {func(*testing.T, *Layer) {}, privately("now"), map[string][]string{"op": {"quick"}}},
	} {
		l, r, chat, local := originSetup(t)
		c.before(t, l)
		chat.take()
		r.atRoute = func(m core.Message) {
			if m.Text == "now" {
				say(t, l, "quick")
			}
		}
		fromChat(t, l, c.in)
		assert.Equal(t, c.want, chat.take(), name)
		assert.Empty(t, local.texts(), name)
	}
}

func TestAFailedDeliveryLeavesAnOriginThatMovedSince(t *testing.T) {
	l, _, _, _ := originSetup(t)
	group := origin{channel: "chat", conversation: "-5"}
	claims := l.claimOrigin(Inbound{Source: "chat", Conversation: "op"}, []core.Profile{{Name: "a"}})
	l.mu.Lock()
	l.origins["a"] = group // another message moved it while this one was delivered
	l.mu.Unlock()
	l.settleOrigin(claims, []route.Result{{Agent: "a", Err: errors.New("down")}})
	assert.Equal(t, group, l.origins["a"])
}
