package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// scopeSetup is a layer with the channel "chat" scoped to conv (empty for the
// owner's private conversation), owned by alice, whose sender is op. carol is
// flagged operator too, as op3, but owns nothing. The groups -5 and -6 are both
// admitted, so only the scope stands between them and the channel.
func scopeSetup(t *testing.T, conv string) (*Layer, *fakeRouter, *convChannel, *fakeRecorder) {
	t.Helper()
	l, r, id, rc := chanSetup(t)
	id.bound["chat\x00op3"] = "carol"
	id.operators["carol"] = true
	l.SetGroups(map[string]map[string]Group{"chat": {
		"-5": {Name: "team", Policy: PolicyOperators},
		"-6": {Name: "other", Policy: PolicyOperators},
	}})
	l.SetScopes(map[string]Scope{"chat": {Owner: "alice", Conversation: conv}})
	chat := &convChannel{name: "chat", l: l, got: map[string][]string{}}
	l.AddChannel(chat)
	return l, r, chat, rc
}

func receive(t *testing.T, l *Layer, in ChannelInbound) ChannelResult {
	t.Helper()
	res, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	return res
}

func from(sender, conv, text string) ChannelInbound {
	in := ChannelInbound{Channel: "chat", Sender: sender, Text: text, Target: byName("a"), Raw: text}
	if conv != "" {
		in.Group, in.Conversation = true, conv
	}
	return in
}

func TestAPrivateChannelActsOnItsOwnerInPrivateAloneAndAnswersOnlyThere(t *testing.T) {
	l, r, chat, rc := scopeSetup(t, "")
	for _, in := range []ChannelInbound{
		from("op3", "", "another operator, privately"),
		from("op", "-5", "the owner, in an admitted group"),
		from("op3", "-6", "another operator, in another admitted group"),
		from("stranger", "-9", "a stranger, in a group the bot was added to"),
	} {
		assert.Equal(t, ChannelResult{}, receive(t, l, in), in.Text)
	}
	assert.Empty(t, r.routed, "none of it reaches an agent")
	assert.Equal(t, []rec{
		{"chat", "op3", RecordMessage, "not the channel's owner", "another operator, privately"},
		{"chat", "op", RecordMessage, "not the channel's conversation: -5", "the owner, in an admitted group"},
		{"chat", "op3", RecordMessage, "not the channel's conversation: -6", "another operator, in another admitted group"},
		{"chat", "stranger", RecordMessage, "not the channel's conversation: -9", "a stranger, in a group the bot was added to"},
	}, rc.got, "each is recorded, with its sender and its whole text")

	require.NotNil(t, receive(t, l, from("op", "", "hi")).Delivery)
	require.Len(t, r.routed, 1)
	say(t, l, "hello")
	_, err := l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"hello", "allow?"}}, chat.take(), "the owner's private conversation alone: no group, no other operator")
}

func TestAGroupChannelActsOnItsOwnerInThatGroupAloneAndAnswersOnlyThere(t *testing.T) {
	l, r, chat, rc := scopeSetup(t, "-5")
	for _, in := range []ChannelInbound{
		from("op3", "-5", "another operator, in the channel's group"),
		from("stranger", "-5", "a stranger, in the channel's group"),
		from("op", "-6", "the owner, in another admitted group"),
		from("op", "", "the owner, privately"),
		from("op", "-5/42", "the owner, in a topic of the channel's group"),
	} {
		assert.Equal(t, ChannelResult{}, receive(t, l, in), in.Text)
	}
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{
		{"chat", "op3", RecordMessage, "not the channel's owner", "another operator, in the channel's group"},
		{"chat", "stranger", RecordMessage, "no subject", "a stranger, in the channel's group"},
		{"chat", "op", RecordMessage, "not the channel's conversation: -6", "the owner, in another admitted group"},
		{"chat", "op", RecordMessage, "not the channel's conversation: private", "the owner, privately"},
		{"chat", "op", RecordMessage, "not the channel's conversation: -5/42", "the owner, in a topic of the channel's group"},
	}, rc.got)

	require.NotNil(t, receive(t, l, from("op", "-5", "hi")).Delivery)
	say(t, l, "hello")
	_, err := l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"-5": {"hello"}, "op": {"allow?"}}, chat.take(),
		"the agent's answer in the group alone; pao's question to the owner privately, where no one else reads it")
}

func TestATopicChannelActsOnItsOwnerInThatTopicAloneAndShowsEverythingThere(t *testing.T) {
	l, r, chat, rc := scopeSetup(t, "-5/42")
	for _, conv := range []string{"-5", "-5/7", "-6/42"} {
		assert.Equal(t, ChannelResult{}, receive(t, l, from("op", conv, "hi")), conv)
	}
	assert.Empty(t, r.routed)
	assert.Len(t, rc.got, 3)

	say(t, l, "unasked")
	assert.Equal(t, map[string][]string{"-5/42": {"unasked"}}, chat.take(), "output with no origin goes to the topic, not the group as a whole")

	require.NotNil(t, receive(t, l, from("op", "-5/42", "hi")).Delivery)
	say(t, l, "hello")
	assert.Equal(t, map[string][]string{"-5/42": {"hello"}}, chat.take())
}

func TestAChannelWithNoScopeActsOnNothingAndShowsNothing(t *testing.T) {
	l, r, chat, rc := scopeSetup(t, "")
	l.SetScopes(nil)
	for _, conv := range []string{"", "-5"} {
		assert.Equal(t, ChannelResult{}, receive(t, l, from("op", conv, "hi")), conv)
	}
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "channel has no owner and conversation", "hi"}, {"chat", "op", RecordMessage, "channel has no owner and conversation", "hi"}}, rc.got)
	say(t, l, "hello")
	_, err := l.Send(context.Background(), "a", core.Message{Sender: "pao", Kind: core.MessageSystemEvent, Text: "notice"})
	require.NoError(t, err)
	assert.Empty(t, chat.take())
}

func TestOnlyTheOwnersPressIsTakenInPrivateAndOnlyAPressThere(t *testing.T) {
	l, _, _, rc := scopeSetup(t, "-5")
	press := from("op", "", "/pao help")
	press.Press = true
	assert.NotEmpty(t, receive(t, l, press).Reply, "the owner answers what pao asked privately")
	press.Sender = "op3"
	assert.Equal(t, ChannelResult{}, receive(t, l, press))
	assert.Equal(t, []rec{{"chat", "op3", RecordMessage, "not the channel's owner", "/pao help"}}, rc.got)
}

func TestOnAGroupChannelABindingToTheOwnerPrivatelyShowsNoAgentOutputThere(t *testing.T) {
	l, _, chat, _ := scopeSetup(t, "-5")
	l.SetBindings([]Binding{bind("private:alice", 3, "")})
	require.NotNil(t, receive(t, l, from("op", "-5", "hi")).Delivery)
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"-5": {"x@1"}}, chat.take(), "an agent's output stays in the channel's conversation, whatever binds it elsewhere")
}

func TestARejectionIsRecordedWithTheSendersNameAsThePlatformGaveIt(t *testing.T) {
	l, _, _, rc := scopeSetup(t, "-5")
	in := from("stranger", "-5", "hi")
	in.SenderName = "Mallory @mal"
	receive(t, l, in)
	assert.Equal(t, []string{"Mallory @mal"}, rc.names)
}
