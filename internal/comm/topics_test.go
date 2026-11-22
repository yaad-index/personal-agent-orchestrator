package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// topicSetup is originSetup with the group -5 ("team") split into topics, of which
// topic 42 is named "games".
func topicSetup(t *testing.T) (*Layer, *fakeRouter, *convChannel, *fakeRecorder) {
	t.Helper()
	l, r, chat, _ := originSetup(t)
	l.SetGroups(map[string]map[string]Group{"chat": {
		"-5":                          {Name: "team", Policy: PolicyOperators},
		TopicConversation("-5", "42"): {Name: "games", Policy: PolicyOperators, Topic: true},
	}})
	return l, r, chat, l.recorder.(*fakeRecorder)
}

func TestATopicsConversationIsTheGroupAndTheTopicTogether(t *testing.T) {
	assert.Equal(t, "-5/42", TopicConversation("-5", "42"))
	for conv, want := range map[string][2]string{"-5/42": {"-5", "42"}} {
		g, top, ok := SplitTopic(conv)
		assert.True(t, ok)
		assert.Equal(t, want, [2]string{g, top})
	}
	for _, conv := range []string{"-5", "op", "", "/42", "-5/"} {
		_, _, ok := SplitTopic(conv)
		assert.False(t, ok, "%q is not a topic's conversation", conv)
	}
}

func TestEveryTopicOfAnAdmittedGroupIsServedNamedOrNot(t *testing.T) {
	l, r, _, rc := topicSetup(t)
	for _, conv := range []string{"-5/42", "-5/77"} {
		res, err := l.ReceiveFromChannel(context.Background(), inGroup(conv, "op", "hi"))
		require.NoError(t, err, conv)
		require.NotNil(t, res.Delivery, conv)
	}
	assert.Len(t, r.routed, 2)
	assert.Empty(t, rc.got)
}

func TestATopicIsReadByItsGroupsReadersAndRefusedWithItsGroup(t *testing.T) {
	l, r, _, rc := topicSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5/77", "plain", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res, "the group's member policy holds in its topics")
	res, err = l.ReceiveFromChannel(context.Background(), inGroup("-6/42", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res, "a topic of a group that is not admitted is refused")
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{
		{"chat", "plain", RecordMessage, "outside access in group team", "RAW"},
		{"chat", "op", RecordMessage, "group -6/42", "RAW"},
	}, rc.got)
}

// A group that moves to a new id takes its topics with it out of admission: the
// new id's topics are refused until the operator lists the new id.
func TestAMovedGroupsTopicsAreRefusedUntilTheNewIdIsListed(t *testing.T) {
	l, r, _, rc := topicSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "-5", Group: true, Conversation: "-5", MovedTo: "-9"})
	require.NoError(t, err)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-9/42", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{
		{"chat", "-5", RecordMessage, "conversation moved", "group team: from -5 to -9"},
		{"chat", "op", RecordMessage, "group -9/42", "RAW"},
	}, rc.got)
}

func TestAReplyGoesBackToTheTopicItWasAskedIn(t *testing.T) {
	l, _, chat, _ := topicSetup(t)
	fromChat(t, l, inGroup("-5/42", "op", "named"))
	say(t, l, "one")
	assert.Equal(t, map[string][]string{"-5/42": {"one"}}, chat.take(), "a named topic")
	fromChat(t, l, inGroup("-5/77", "op", "unnamed"))
	say(t, l, "two")
	assert.Equal(t, map[string][]string{"-5/77": {"two"}}, chat.take(), "a topic that isn't named is its own conversation too")
	fromChat(t, l, inGroup("-5", "op", "general"))
	say(t, l, "three")
	assert.Equal(t, map[string][]string{"-5": {"three"}}, chat.take(), "the group's own conversation, as before")
}

func TestABindingToANamedTopicSendsIntoThatTopic(t *testing.T) {
	l, _, chat, _ := topicSetup(t)
	l.SetBindings([]Binding{bind("group:games", 2, "")})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"x@1"}, "-5/42": {"x@2"}}, chat.take())
}

func TestANamedTopicIsNotOneOfTheChannelsConversationsAsAWhole(t *testing.T) {
	l, _, chat, _ := topicSetup(t)
	say(t, l, "no origin")
	assert.Equal(t, map[string][]string{"-5": {"no origin"}}, chat.take(), "output with no origin reaches the group, not each named topic again")
}

func TestAConversationsOwnDefaultComesBeforeTheChannels(t *testing.T) {
	l, r, _, _ := topicSetup(t)
	for _, a := range []string{"g", "t", "c"} {
		r.agents[a] = []string{a}
	}
	l.SetDefaults(mapDefaults{"chat": "c", DefaultKey("team"): "g", DefaultKey("games"): "t"})
	for conv, want := range map[string]string{"-5/42": "t", "-5/77": "g", "-5": "g"} {
		in := inGroup(conv, "op", "hi")
		in.Target = core.Target{}
		_, err := l.ReceiveFromChannel(context.Background(), in)
		require.NoError(t, err, conv)
		assert.Equal(t, []string{want}, r.to[len(r.to)-1], conv)
	}
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, r.to[len(r.to)-1], "a private conversation has the channel's")

	a, ok := l.DefaultAgentIn("chat", "-5/42")
	assert.True(t, ok)
	assert.Equal(t, "t", a)
}

func TestANamedTopicWithNoDefaultOfItsOwnHasTheChannelsNotItsGroups(t *testing.T) {
	l, _, _, _ := topicSetup(t)
	l.SetDefaults(mapDefaults{"chat": "c", DefaultKey("team"): "g"})
	a, _ := l.DefaultAgentIn("chat", "-5/42")
	assert.Equal(t, "c", a)
	a, _ = l.DefaultAgentIn("chat", "-5/77")
	assert.Equal(t, "g", a, "a topic that isn't named is its group's")
	a, _ = l.DefaultAgentIn("chat", "op")
	assert.Equal(t, "c", a)
}

func TestWithNoConversationDefaultRoutingIsAsBefore(t *testing.T) {
	l, _, _, _ := topicSetup(t)
	l.SetDefaults(mapDefaults{"chat": "c"})
	for _, conv := range []string{"-5", "-5/42", "-5/77", "op", ""} {
		a, ok := l.DefaultAgentIn("chat", conv)
		assert.True(t, ok, conv)
		assert.Equal(t, "c", a, conv)
	}
	_, ok := l.DefaultAgentIn("other", "-5")
	assert.False(t, ok)
}
