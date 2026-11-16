package comm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type fakeIdentity struct {
	bound     map[string]string // channel NUL sender -> subject
	operators map[string]bool
	secret    string
	registers int
}

func (f *fakeIdentity) Resolve(ch, snd string) (string, bool) {
	s, ok := f.bound[ch+"\x00"+snd]
	return s, ok
}
func (f *fakeIdentity) Register(ch, snd, secret string) (string, bool) {
	f.registers++
	if secret != f.secret {
		return "", false
	}
	f.bound[ch+"\x00"+snd] = "alice"
	return "alice", true
}
func (f *fakeIdentity) IsOperator(s string) bool { return f.operators[s] }
func (f *fakeIdentity) Operators(ch string) []string {
	var out []string
	for k, subj := range f.bound {
		if c, snd, _ := strings.Cut(k, "\x00"); c == ch && f.operators[subj] {
			out = append(out, snd)
		}
	}
	return out
}

type rec struct{ channel, sender, kind, reason, payload string }

type fakeRecorder struct {
	got []rec
	err error
}

func (f *fakeRecorder) Record(ch, snd, kind, reason, payload string) error {
	f.got = append(f.got, rec{ch, snd, kind, reason, payload})
	return f.err
}

func chanSetup(t *testing.T) (*Layer, *fakeRouter, *fakeIdentity, *fakeRecorder) {
	t.Helper()
	l, r, _ := setup(t)
	id := &fakeIdentity{bound: map[string]string{"chat\x00op": "alice", "chat\x00plain": "bob"}, operators: map[string]bool{"alice": true}, secret: "SEC"}
	rc := &fakeRecorder{}
	l.SetIdentity(id, rc)
	return l, r, id, rc
}

func TestAnOperatorSubjectIsRoutedAsItselfAndAttributedUnderItsName(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Empty(t, res.Reply)
	require.Len(t, r.routed, 1)
	assert.Equal(t, "[alice]: hi", r.routed[0].Line(), "the agent reads the subject, not the external id")
	assert.Empty(t, rc.got, "nothing rejected")
}

func TestASubjectThatIsNotFlaggedIsRecordedAndNeverRouted(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "plain", Text: "hi", Target: byName("a"), Raw: "RAW"})
	require.NoError(t, err)
	assert.Nil(t, res.Delivery)
	assert.Equal(t, ReplyNoAccess, res.Reply)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "plain", RecordMessage, "outside access", "RAW"}}, rc.got)
}

func TestAnUnboundSenderIsRecordedAnsweredWithNothingAndNeverRouted(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "stranger", Text: "hi", Target: byName("a"), Raw: "RAW"})
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
	assert.Equal(t, []rec{{"chat", "stranger", RecordMessage, "no subject", "RAW"}}, rc.got)
}

func TestTheSameIdOnAnotherChannelIsUnbound(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "other", Sender: "op", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	assert.Empty(t, r.routed)
	require.Len(t, rc.got, 1)
	assert.Equal(t, "no subject", rc.got[0].reason)
}

func TestTheOperatorFlagIsReadAtEachMessage(t *testing.T) {
	l, r, id, _ := chanSetup(t)
	in := ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a")}
	_, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, r.routed, 1)

	id.operators["alice"] = false
	res, err := l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	assert.Nil(t, res.Delivery, "cleared, it stops at once")
	assert.Len(t, r.routed, 1)

	delete(id.bound, "chat\x00op")
	id.operators["alice"] = true
	res, err = l.ReceiveFromChannel(context.Background(), in)
	require.NoError(t, err)
	assert.Nil(t, res.Delivery, "and so does a revoked binding")
	assert.Len(t, r.routed, 1)
}

func TestARegistrationGetsOneOfTwoFixedRepliesAndIsRecordedWithoutItsSecret(t *testing.T) {
	l, r, id, rc := chanSetup(t)
	ctx := context.Background()

	res, err := l.ReceiveFromChannel(ctx, ChannelInbound{Channel: "chat", Sender: "new", Registration: true, Text: "WRONG", Raw: "WRONG"})
	require.NoError(t, err)
	assert.Equal(t, ReplyRefused, res.Reply)

	res, err = l.ReceiveFromChannel(ctx, ChannelInbound{Channel: "chat", Sender: "new", Registration: true, Text: "SEC", Raw: "SEC"})
	require.NoError(t, err)
	assert.Equal(t, ReplyRegistered, res.Reply)

	assert.Equal(t, []rec{
		{"chat", "new", RecordRegistration, "not accepted", ""},
		{"chat", "new", RecordRegistration, "registered", ""},
	}, rc.got, "what was presented is never handed to the record")
	assert.Equal(t, 2, id.registers)
	assert.Empty(t, r.routed, "a registration is never routed to an agent")
}

func TestARegistrationFromABoundOrUnboundSenderIsAnsweredTheSameWay(t *testing.T) {
	l, _, _, _ := chanSetup(t)
	ctx := context.Background()
	bound, err := l.ReceiveFromChannel(ctx, ChannelInbound{Channel: "chat", Sender: "op", Registration: true, Text: "WRONG"})
	require.NoError(t, err)
	unbound, err := l.ReceiveFromChannel(ctx, ChannelInbound{Channel: "chat", Sender: "nobody", Registration: true, Text: "WRONG"})
	require.NoError(t, err)
	assert.Equal(t, bound, unbound)
}

func TestWithoutAnIdentityStoreNothingIsAccepted(t *testing.T) {
	l, r, _ := setup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a")})
	assert.ErrorIs(t, err, ErrNoIdentity)
	assert.Empty(t, r.routed)
}

func TestAFailureToRecordIsReturnedAndNeverSentToTheSender(t *testing.T) {
	l, _, _, rc := chanSetup(t)
	rc.err = errors.New("disk full")
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "stranger", Text: "hi", Target: byName("a")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
	assert.Empty(t, res.Reply, "the sender is told nothing of it")
}

func TestAnOperatorMessageToNoAgentIsAnErrorForTheAdapter(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("nobody")})
	require.Error(t, err)
	assert.Empty(t, r.routed)
	assert.Empty(t, rc.got, "it is not a rejection: the sender is allowed, the target is not there")
}

func TestAChannelsInboundRunsThatChannelsPreprocessorAndKeepsItsReplyReference(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	l.Preprocess("chat", upper{})
	first, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "question?"})
	require.NoError(t, err)

	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Target: byName("a"), Reply: first.ID})
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Equal(t, "voice: yes", res.Delivery.Message.Text, "the source is the channel, so its preprocessor ran")
	assert.Equal(t, first.ID, res.Delivery.Message.Reply)
	require.Len(t, r.routed, 1)

	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Target: byName("a"), ReplyQuote: "question?"})
	require.NoError(t, err)
	assert.Equal(t, first.ID, res.Delivery.Message.Reply, "a quote resolves too")
}

func TestARejectionWithNoRecorderIsStillSilentAndSafe(t *testing.T) {
	l, r, id, _ := chanSetup(t)
	l.SetIdentity(id, nil)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "stranger", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res)
	assert.Empty(t, r.routed)
}

func TestRecipientsAreTheOperatorFlaggedSendersOnTheChannel(t *testing.T) {
	l, _, id, _ := chanSetup(t)
	id.bound["other\x00op2"] = "alice"
	assert.Equal(t, []string{"op"}, l.Recipients("chat"), "bob is bound but not flagged: nothing is sent him")
	assert.Equal(t, []string{"op2"}, l.Recipients("other"))
	assert.Empty(t, l.Recipients("none"))

	bare, _, _ := setup(t)
	assert.Empty(t, bare.Recipients("chat"), "no identity store, no recipients")
}

func TestAReplyWithNoAddressGoesToTheAgentThatWroteTheMessage(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	q, err := l.Send(context.Background(), "a", core.Message{Sender: "a", Text: "question?"})
	require.NoError(t, err)

	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "yes", Reply: q.ID})
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Equal(t, [][]string{{"a"}}, r.to)

}

func TestAnUnaddressedMessageThatIsNotAReplyIsNotRouted(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hi"})
	require.Error(t, err)
	assert.Empty(t, r.routed)
	assert.Empty(t, rc.got)

	own, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "first", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "re", Reply: own.Delivery.Message.ID})
	require.Error(t, err, "a reply to one's own message names no agent")
	assert.Equal(t, "", r.resolved[len(r.resolved)-1], "and nothing was guessed from the subject's name")
}

func TestAnAddressedReplyGoesWhereItWasAddressedNotToTheAuthor(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	r.agents["b"] = []string{"b"}
	m, err := l.store.Add(core.Message{Sender: "b", Text: "from b"}, "a", "b")
	require.NoError(t, err)
	_, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "re", Reply: m.ID, Target: byName("a")})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"a"}}, r.to)
}
