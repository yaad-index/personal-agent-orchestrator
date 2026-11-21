package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestWhatTheGovernorSaysGoesToTheRequesterAlone(t *testing.T) {
	l, _, chat, local := governorSetup(t)
	l.SetBindings([]Binding{{Agent: "a", Stream: StreamStdout, To: Dest{Kind: "group", Name: "team"}, Level: 1}})
	require.NoError(t, l.Bind("a", "local"))

	fromChat(t, l, privately("/gpao pause the coder"))
	say(t, l, "use the acme folder?")
	assert.Equal(t, map[string][]string{"op": {"use the acme folder?"}}, chat.take(), "privately, to the requester: no binding, no group")
	assert.Empty(t, local.texts(), "nor the channel it is bound to")

	fromChat(t, l, inGroup("-5", "op", "/gpao pause the coder"))
	say(t, l, "use the acme folder?")
	assert.Equal(t, map[string][]string{"op": {"use the acme folder?"}}, chat.take(), "asked in the group, answered privately")
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
