package comm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// governorSetup is originSetup with the agent a as the node's governor.
func governorSetup(t *testing.T) (*Layer, *fakeRouter, *convChannel, *recChannel) {
	t.Helper()
	l, r, chat, local := originSetup(t)
	l.SetGovernor("a", "")
	return l, r, chat, local
}

func TestAGovernorRequestGoesToTheGovernorAloneAsTheSendersMessage(t *testing.T) {
	l, r, _, _ := governorSetup(t)
	r.agents["b"] = []string{"b"}
	for _, text := range []string{"/gpao pause the coder", "/gpao@bot pause the coder", "  /gpao   pause the coder  "} {
		r.routed, r.to = nil, nil
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: text, Target: byName("b")})
		require.NoError(t, err, text)
		require.NotNil(t, res.Delivery, text)
		assert.Nil(t, res.Addressed, "%q: the sender's last address is left as it was", text)
		assert.Equal(t, [][]string{{"a"}}, r.to, "%q: to the governor, whatever target the adapter brings", text)
		require.Len(t, r.routed, 1)
		assert.Equal(t, "[alice]: pause the coder", r.routed[0].Line(), text)
	}
}

func TestWhatTheGovernorSaysGoesBackWhereItWasAsked(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	l.SetBindings([]Binding{{Agent: "a", Stream: StreamStdout, To: Dest{Kind: "group", Name: "team"}, Level: 1}})
	require.NoError(t, l.Bind("a", "local"))

	fromChat(t, l, privately("/gpao pause the coder"))
	say(t, l, "use the acme folder?")
	assert.Equal(t, map[string][]string{"op": {"use the acme folder?"}}, chat.take(), "asked privately, answered privately: no binding, no group")
	assert.Empty(t, local.texts(), "nor the channel it is bound to")

	fromChat(t, l, inGroup("-5", "op", "/gpao pause the coder"))
	say(t, l, "use the acme folder?")
	assert.Equal(t, map[string][]string{"-5": {"use the acme folder?"}}, chat.take(), "asked in the group, answered in the group, and not privately")
	assert.Empty(t, local.texts())
}

func TestAGovernorRequestFromTheLocalChannelIsAnsweredThere(t *testing.T) {
	l, r, chat, local := governorSetup(t)
	r.agents["b"] = []string{"b"}
	reply, d, ok, err := l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/gpao list my agents", Target: byName("b")})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, reply)
	require.NotNil(t, d)
	assert.Equal(t, [][]string{{"a"}}, r.to)
	assert.Equal(t, "[human]: list my agents", r.routed[0].Line())
	say(t, l, "/pao agents")
	assert.Equal(t, []string{"/pao agents"}, local.texts())
	assert.Empty(t, chat.take())
}

func TestTheGovernorWithNoRequesterSpeaksToNoOne(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	say(t, l, "with no turn at all")
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	say(t, l, "in a turn another agent started")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestAGovernorRequestThatCannotGoIsAnswered(t *testing.T) {
	l, r, _, _ := originSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), privately("/gpao pause the coder"))
	require.NoError(t, err)
	assert.Equal(t, ReplyNoGovernor, res.Reply)
	reply, _, ok, err := l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/gpao pause it"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, ReplyNoGovernor, reply)

	l.SetGovernor("a", "")
	for _, text := range []string{"/gpao", "/gpao   ", "/gpao@bot"} {
		res, err = l.ReceiveFromChannel(context.Background(), privately(text))
		require.NoError(t, err)
		assert.Equal(t, "usage: /gpao <what you want done, in your own words>", res.Reply, "%q", text)
	}
	assert.Empty(t, r.routed)
}

func TestAGovernorRequestFromASubjectThatIsNotTheOperatorIsRefused(t *testing.T) {
	l, r, _, _ := governorSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "plain", Text: "/gpao pause the coder"})
	require.NoError(t, err)
	assert.Equal(t, ReplyNoAccess, res.Reply)
	assert.Empty(t, r.routed)
}

func TestTextThatOnlyLooksLikeAGovernorRequestIsNotOne(t *testing.T) {
	l, r, _, _ := governorSetup(t)
	for _, text := range []string{"/gpaox hi", "gpao hi", "hi /gpao", "/pao gpao hi"} {
		_, ok := l.governorRequest(text)
		assert.False(t, ok, "%q", text)
	}
	r.agents["b"] = []string{"b"}
	fromChat(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "/gpaox hi", Target: byName("b")})
	assert.Equal(t, [][]string{{"b"}}, r.to)
}

func TestTheNodeMayNameAnotherGovernorPrefix(t *testing.T) {
	l, r, _, _ := originSetup(t)
	l.SetGovernor("a", "/ask")
	r.agents["b"] = []string{"b"}
	fromChat(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "/ask pause the coder", Target: byName("b")})
	fromChat(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "/gpao pause the coder", Target: byName("b")})
	assert.Equal(t, [][]string{{"a"}, {"b"}}, r.to, "the named prefix goes to the governor; the default is then plain text")
	res, err := l.ReceiveFromChannel(context.Background(), privately("/ask"))
	require.NoError(t, err)
	assert.Equal(t, "usage: /ask <what you want done, in your own words>", res.Reply)
}

func TestARequestFromAnotherSubjectStartsTheGovernorAfresh(t *testing.T) {
	l, r, _, _ := governorSetup(t)
	var resets int
	l.SetGovernorReset(func(context.Context) error { resets++; return nil })

	fromChat(t, l, privately("/gpao pause the coder"))
	assert.Equal(t, 1, resets, "the first request since the node started: a session from before is not reused")
	fromChat(t, l, privately("/gpao and the other one"))
	assert.Equal(t, 1, resets, "the same subject keeps its context")

	_, d, _, err := l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "/gpao from here"})
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, 1, resets, "alice on the local channel is the same alice")

	_, d, _, err = l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/gpao list my agents"})
	require.NoError(t, err)
	require.NotNil(t, d, "another subject is served, not refused")
	assert.Equal(t, 2, resets, "on a fresh context")

	fromChat(t, l, privately("/gpao back again"))
	assert.Equal(t, 3, resets, "and alice again on a fresh one")
	assert.Len(t, r.routed, 5)
}

func TestAGovernorThatCannotBeResetIsNotSentTheRequest(t *testing.T) {
	l, r, _, _ := governorSetup(t)
	fail := errors.New("no session")
	l.SetGovernorReset(func(context.Context) error { return fail })
	res, err := l.ReceiveFromChannel(context.Background(), privately("/gpao pause the coder"))
	require.NoError(t, err)
	assert.Equal(t, "the governor could not be started on a fresh context: no session", res.Reply)
	assert.Empty(t, r.routed)

	fail = nil
	fromChat(t, l, privately("/gpao pause the coder"))
	assert.Len(t, r.routed, 1, "the next request tries again")
}

func TestARequestThatDoesNotReachTheGovernorDoesNotResetIt(t *testing.T) {
	l, _, _, _ := governorSetup(t)
	var resets int
	l.SetGovernorReset(func(context.Context) error { resets++; return nil })
	res, err := l.ReceiveFromChannel(context.Background(), privately("/gpao"))
	require.NoError(t, err)
	assert.Contains(t, res.Reply, "usage")
	assert.Zero(t, resets)
}

func TestPaosMessagesAboutTheGovernorGoToItsRequesterAlone(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	fromChat(t, l, inGroup("-5", "op", "/gpao pause the coder"))
	m, err := l.NotifyAgent(context.Background(), "a", core.Message{Sender: "pao", Text: "not ready", Actions: []core.Action{{Label: "ALLOW", Command: "/pao allow q1"}}})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"not ready"}}, chat.take(), "privately to the requester, though asked in the group")
	assert.Empty(t, local.texts(), "not every operator's conversation, as the default would send it")
	assert.Equal(t, core.MessageSystemEvent, m.Kind)

	_, _, _, err = l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/gpao list my agents"})
	require.NoError(t, err)
	_, err = l.NotifyAgent(context.Background(), "a", core.Message{Sender: "pao", Text: "asked locally"})
	require.NoError(t, err)
	assert.Equal(t, []string{"asked locally"}, local.texts(), "a local request: the local channel")
	assert.Empty(t, chat.take())

	_, err = l.NotifyAgent(context.Background(), "b", core.Message{Sender: "pao", Text: "about b"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"about b"}}, chat.take(), "another agent keeps the default")
	assert.Equal(t, []string{"asked locally", "about b"}, local.texts())
}

func TestWithNoRequesterNoOneIsToldAboutTheGovernor(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	_, err := l.NotifyAgent(context.Background(), "a", core.Message{Sender: "pao", Text: "not ready"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serving no request")

	_, err = l.ReceiveAsOperator(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.NotifyAgent(context.Background(), "a", core.Message{Sender: "pao", Text: "in another agent's turn"})
	require.Error(t, err)
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestARequesterWithNoPrivateConversationIsToldNothingAboutTheGovernor(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "chat", Sender: "nobody", Conversation: "-5", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.NotifyAgent(context.Background(), "a", core.Message{Sender: "pao", Text: "not ready"})
	require.Error(t, err, "no conversation of the requester: an error, so a question is refused")
	assert.Contains(t, err.Error(), "nobody has no private conversation")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestAGovernorReplyAGroupMayNotSeeIsHeldBackAndNotRedirected(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	fromChat(t, l, inGroup("-5", "op", "/gpao pause the coder"))
	l.SetOperatorAccess(func(p core.Profile) bool { return p.Name != "a" })
	say(t, l, "use the acme folder?")
	assert.Empty(t, chat.take(), "the group's readers may not reach the governor: not there, and not to the requester privately either")
	assert.Empty(t, local.texts())

	_, err := l.Propose(context.Background(), "a", proposal("/pao agents"))
	assert.NoError(t, err, "pao's own message: the operators' to see")
	assert.Equal(t, map[string][]string{"-5": {"/pao agents"}}, chat.take())
}
