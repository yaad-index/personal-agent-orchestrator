package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupSetup is chanSetup with group "-5" admitted on channel "chat" under the
// operators policy.
func groupSetup(t *testing.T) (*Layer, *fakeRouter, *fakeIdentity, *fakeRecorder) {
	t.Helper()
	l, r, id, rc := chanSetup(t)
	l.SetGroups(map[string]map[string]string{"chat": {"-5": PolicyOperators}})
	return l, r, id, rc
}

func inGroup(conv, sender, text string) ChannelInbound {
	return ChannelInbound{Channel: "chat", Sender: sender, Text: text, Target: byName("a"), Raw: "RAW", Group: true, Conversation: conv}
}

func TestAnOperatorInAnAdmittedGroupIsRoutedAsInAPrivateConversation(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "hi"))
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Empty(t, res.Reply)
	require.Len(t, r.routed, 1)
	assert.Equal(t, "[alice]: hi", r.routed[0].Line(), "attributed to the sender's subject, not the group")
	assert.Empty(t, rc.got)
}

func TestAGroupThatIsNotAdmittedIsRefusedWhoeverSends(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	for _, in := range []ChannelInbound{
		inGroup("-6", "op", "hi"), // another group on the same channel
		inGroup("", "op", "hi"),   // a group the adapter could not name
		{Channel: "other", Sender: "op", Text: "hi", Target: byName("a"), Raw: "RAW", Group: true, Conversation: "-5"}, // admitted on another channel only
	} {
		res, err := l.ReceiveFromChannel(context.Background(), in)
		require.NoError(t, err)
		assert.Equal(t, ChannelResult{}, res)
	}
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "group", "RAW"}, {"chat", "op", RecordMessage, "group", "RAW"}, {"other", "op", RecordMessage, "group", "RAW"}}, rc.got)
}

func TestWithNoGroupsAdmittedEveryGroupIsRefused(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "group", "RAW"}}, rc.got)
}

func TestInAnAdmittedGroupAnyoneThePolicyDoesNotAdmitIsRecordedAndNotAnswered(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	for _, sender := range []string{"plain", "stranger"} {
		res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", sender, "hi"))
		require.NoError(t, err)
		assert.Equal(t, ChannelResult{}, res, "%s: nothing is said in the group, not even a refusal", sender)
	}
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "plain", RecordMessage, "outside access", "RAW"}, {"chat", "stranger", RecordMessage, "no subject", "RAW"}}, rc.got)
}

func TestTheOperatorFlagIsReadAtEachGroupMessage(t *testing.T) {
	l, r, id, _ := groupSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "hi"))
	require.NoError(t, err)
	require.Len(t, r.routed, 1)

	id.operators["alice"] = false
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Len(t, r.routed, 1)
}

func TestARegistrationInAnAdmittedGroupIsRefusedNotKeptAndNotAnswered(t *testing.T) {
	l, _, id, rc := groupSetup(t)
	in := inGroup("-5", "new", "SEC")
	in.Registration, in.Raw = true, ""
	res, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Zero(t, id.registers, "the secret is never tried")
	_, bound := id.bound["chat\x00new"]
	assert.False(t, bound)
	assert.Equal(t, []rec{{"chat", "new", RecordRegistration, "from a group", ""}}, rc.got)
}

func TestACommandInAnAdmittedGroupIsAnsweredThere(t *testing.T) {
	l, r, _, _ := groupSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "/pao help"))
	require.NoError(t, err)
	assert.NotEmpty(t, res.Reply)
	assert.Empty(t, r.routed)
}

func TestAPressInAnAdmittedGroupIsTheOperatorsAndOneFromAnyoneElseIsRecorded(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	in := inGroup("-5", "op", "not a command")
	in.Press = true
	res, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, ReplyNotACommand, res.Reply, "the operator's press is checked as in a private conversation")

	in.Sender = "plain"
	res, err = l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "press not a command", "RAW"}, {"chat", "plain", RecordMessage, "outside access", "RAW"}}, rc.got)
}

func TestAJoinThePolicyDoesNotAdmitIsRecordedAndAnAdmittedOneIsNot(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	for _, sender := range []string{"op", "plain", "stranger"} {
		in := ChannelInbound{Channel: "chat", Sender: sender, Group: true, Conversation: "-5", Join: true}
		res, err := l.ReceiveFromChannel(context.Background(), in)
		require.NoError(t, err)
		assert.Equal(t, ChannelResult{}, res)
	}
	// A join in a group that is not admitted is not news: everything there is refused.
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "stranger", Group: true, Conversation: "-6", Join: true})
	require.NoError(t, err)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "plain", RecordMessage, "joined outside policy", "conversation -5"}, {"chat", "stranger", RecordMessage, "joined outside policy", "conversation -5"}}, rc.got)
}

func TestAConversationThatMovedIsRecordedWithBothIdsAndTheNewIdIsNotAdmitted(t *testing.T) {
	l, r, _, rc := groupSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "-5", Group: true, Conversation: "-5", MovedTo: "-1009"})
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Equal(t, []rec{{"chat", "-5", RecordMessage, "conversation moved", "from -5 to -1009"}}, rc.got)

	_, err = l.ReceiveFromChannel(context.Background(), inGroup("-1009", "op", "hi"))
	require.NoError(t, err)
	assert.Empty(t, r.routed, "fails closed until the operator lists the new id")
}

func TestAPolicyTheLayerDoesNotKnowAdmitsNoOne(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	l.SetGroups(map[string]map[string]string{"chat": {"-5": "everyone"}})
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "outside access", "RAW"}}, rc.got)
}

func TestAGroupWithNoConversationIdIsNeverAdmittedEvenIfAnEmptyIdIsListed(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	l.SetGroups(map[string]map[string]string{"chat": {"": PolicyOperators}})
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("", "op", "hi"))
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed, "a press on no message reaches the layer with no conversation, and must stay refused")
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "group", "RAW"}}, rc.got)
}
