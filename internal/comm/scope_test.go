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

func TestAChannelReachesItsOwnAgentAndNoOther(t *testing.T) {
	l, r, _, _ := scopeSetup(t, "")
	r.agents["b"] = []string{"b"}
	l.SetCommands(&cmdOps{})

	require.NotNil(t, receive(t, l, from("op", "", "plain text")).Delivery, "plain text goes to the channel's agent")
	assert.Equal(t, [][]string{{"a"}}, r.to)

	for _, text := range []string{"/pao agent b hi", "/pao b hi", "/pao tag team hi"} {
		in := from("op", "", text)
		in.Target = core.Target{}
		res := receive(t, l, in)
		assert.Nil(t, res.Delivery, text)
		assert.Equal(t, "only a is reached from this channel.", res.Reply, text)
	}
	sticky := from("op", "", "sticky to b")
	sticky.Target = core.Target{Kind: core.ByName, Value: "b"}
	assert.Equal(t, "only a is reached from this channel.", receive(t, l, sticky).Reply, "an address kept from before is refused too")
	assert.Equal(t, [][]string{{"a"}}, r.to, "nothing reached b, nor a and b by their tag")

	l.SetDefaults(mapDefaults{})
	res := receive(t, l, from("op", "", "anyone?"))
	assert.Nil(t, res.Delivery)
	assert.Equal(t, ReplyNoAgent, res.Reply, "a channel with no agent reaches none")
}

func TestAChannelShowsItsOwnAgentsOutputAndNoOthers(t *testing.T) {
	l, r, chat, _ := scopeSetup(t, "-5")
	r.agents["b"] = []string{"b"}
	l.SetBindings([]Binding{{Agent: "b", To: Dest{Kind: DestGroup, Name: "team"}, Level: 1}})
	fromB := func(int) (core.Message, bool) {
		return core.Message{Sender: "b", Kind: core.MessageContent, Text: "from b"}, true
	}
	require.NoError(t, l.SendEntry(context.Background(), "b", 1, fromB, nil))
	say(t, l, "from a")
	assert.Equal(t, map[string][]string{"-5": {"from a"}}, chat.take(), "b's words never reach a's channel, though b is bound there")
}

// providerSetup is a layer whose provider tg carries alice's and bob's private
// channels, alice's group -5 and her topic -6/42, each with an agent of its own.
// alice is op, bob is op3, both on tg.
func providerSetup(t *testing.T) (*Layer, *fakeRouter, *fakeIdentity, *fakeRecorder) {
	t.Helper()
	l, r, id, rc := chanSetup(t)
	id.bound = map[string]string{"tg\x00op": "alice", "tg\x00op3": "bob"}
	id.operators["bob"] = true
	for _, a := range []string{"b", "c", "d"} {
		r.agents[a] = []string{a}
	}
	l.SetScopes(map[string]Scope{
		"dm-alice": {Owner: "alice"}, "dm-bob": {Owner: "bob"},
		"team": {Owner: "alice", Conversation: "-5"}, "games": {Owner: "alice", Conversation: "-6/42"},
	})
	l.SetProviders(map[string]string{"dm-alice": "tg", "dm-bob": "tg", "team": "tg", "games": "tg"})
	l.SetGroups(map[string]map[string]Group{
		"team":  {"-5": {Name: "team", Policy: PolicyOperators}},
		"games": {"-6": {Name: "games-group", Policy: PolicyOperators}},
	})
	l.SetDefaults(mapDefaults{"dm-alice": "a", "dm-bob": "b", "team": "c", "games": "d"})
	l.SetChannelIDs(map[string]int{"dm-alice": 1, "dm-bob": 2, "team": 3, "games": 4})
	return l, r, id, rc
}

func viaProvider(sender, conv, text string) ChannelInbound {
	in := from(sender, conv, text)
	in.Channel, in.Provider, in.Target = "", "tg", core.Target{}
	return in
}

func TestAProvidersInboundIsPlacedInTheChannelThatIsItsConversation(t *testing.T) {
	l, r, _, rc := providerSetup(t)
	for _, c := range []struct{ sender, conv, agent string }{
		{"op", "", "a"},      // alice, privately: her private channel
		{"op3", "", "b"},     // bob, privately: his
		{"op", "-5", "c"},    // alice in the group
		{"op", "-6/42", "d"}, // alice in the topic
	} {
		res := receive(t, l, viaProvider(c.sender, c.conv, "hi"))
		require.NotNil(t, res.Delivery, c)
		assert.Equal(t, []string{c.agent}, r.to[len(r.to)-1], c)
	}
	assert.Empty(t, rc.got)

	for _, in := range []ChannelInbound{
		viaProvider("stranger", "", "who are you"),
		viaProvider("op", "-6/7", "another topic"),
		viaProvider("op", "-9", "another group"),
		viaProvider("op3", "-5", "bob in alice's group"),
	} {
		assert.Equal(t, ChannelResult{}, receive(t, l, in), in.Text)
	}
	assert.Equal(t, []rec{
		{"tg", "stranger", RecordMessage, "no channel is this conversation: private", "who are you"},
		{"tg", "op", RecordMessage, "no channel is this conversation: -6/7", "another topic"},
		{"tg", "op", RecordMessage, "no channel is this conversation: -9", "another group"},
		{"team", "op3", RecordMessage, "not the channel's owner", "bob in alice's group"},
	}, rc.got, "what is no channel's is recorded under the provider's name; a stranger in a channel under the channel's")
}

func TestAnInboundIsPlacedOnlyAmongItsOwnProvidersChannels(t *testing.T) {
	l, r, _, _ := providerSetup(t)
	// another provider's channel, sorting first, in the same group
	l.SetScopes(map[string]Scope{"elsewhere": {Owner: "alice", Conversation: "-5"}, "team": {Owner: "alice", Conversation: "-5"}})
	l.SetProviders(map[string]string{"elsewhere": "tg2", "team": "tg"})
	l.SetGroups(map[string]map[string]Group{"elsewhere": {"-5": {Name: "x", Policy: PolicyOperators}}, "team": {"-5": {Name: "team", Policy: PolicyOperators}}})
	l.SetDefaults(mapDefaults{"elsewhere": "b", "team": "c"})
	require.NotNil(t, receive(t, l, viaProvider("op", "-5", "hi")).Delivery)
	assert.Equal(t, []string{"c"}, r.to[len(r.to)-1], "tg's channel, not tg2's")
}

func TestARegistrationOnAProviderBindsTheSenderThere(t *testing.T) {
	l, _, id, _ := providerSetup(t)
	in := viaProvider("new", "", "SEC")
	in.Registration = true
	assert.Equal(t, ReplyRegistered, receive(t, l, in).Reply)
	assert.Equal(t, "alice", id.bound["tg\x00new"], "bound on the provider, whichever channel the sender writes in")
}

func TestAPrivatePressOnAProviderIsTheOwnersChannelThatShowedIt(t *testing.T) {
	l, _, id, rc := providerSetup(t)
	l.SetScopes(map[string]Scope{"team": {Owner: "alice", Conversation: "-5"}, "games": {Owner: "alice", Conversation: "-6/42"}})
	id.bound["tg\x00op"] = "alice"
	l.SetCompositeWindow(10)
	l.noteComposite("team", "900:7", "m1")
	press := viaProvider("op", "", "not a command")
	press.Press, press.Pressed = true, "900:7"
	assert.Equal(t, ReplyNotACommand, receive(t, l, press).Reply, "alice has no private channel: her press is the channel that asked")
	assert.Equal(t, "team", rc.got[0].channel, "the channel whose message was pressed, not merely the first of hers")

	unknown := viaProvider("op", "", "not a command")
	unknown.Press, unknown.Pressed = true, "900:99"
	receive(t, l, unknown)
	assert.Equal(t, "games", rc.got[1].channel, "a message that can't be told is placed in her first channel, by name")

	text := viaProvider("op", "", "hello")
	assert.Equal(t, ChannelResult{}, receive(t, l, text), "only a press is placed so")
}
