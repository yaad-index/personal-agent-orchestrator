package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// cmdOps records the operations chat commands run.
type cmdOps struct{ calls []string }

func (c *cmdOps) SetStream(_ context.Context, agent, channel string, _ int) error {
	c.calls = append(c.calls, "stream "+agent+" "+channel)
	return nil
}

func (c *cmdOps) View(_ context.Context, agent, action string) error {
	c.calls = append(c.calls, "view "+agent+" "+action)
	return nil
}

func (c *cmdOps) Decide(_ context.Context, id, decision, _ string) error {
	c.calls = append(c.calls, decision+" "+id)
	return nil
}

// A /pao command never reaches an agent, whatever target the adapter brings
// (ADR-0026 point 1); its answer is for the sender (point 5).
func TestACommandIsPaosAndNeverRouted(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	for _, text := range []string{"/pao stream a", "/pao help", "/pao nosuch", "/pao", "/pao view a pause", "/pao allow q1"} {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: text, Target: byName("a")})
		require.NoError(t, err, text)
		assert.Nil(t, res.Delivery, text)
		assert.Nil(t, res.Addressed, text)
		assert.NotEmpty(t, res.Reply, text)
	}
	assert.Empty(t, r.routed, "the sticky target does not carry a command to an agent")
	assert.Equal(t, []string{"stream a chat", "view a pause", "allow q1"}, ops.calls, "a stream with no channel named is bound to the one the command came on")
	assert.Empty(t, rc.got)
}

func TestACommandThatAddressesRoutesItsMessageThere(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao tag team hello all", Target: byName("a")})
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Equal(t, &core.Target{Kind: core.ByTag, Value: "team"}, res.Addressed)
	assert.Equal(t, [][]string{{"a", "b"}}, r.to, "the command's address, not the sticky one")
	require.Len(t, r.routed, 1)
	assert.Equal(t, "[alice]: hello all", r.routed[0].Line(), "the agent reads the message, not the command")

	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao ghost hello"})
	assert.ErrorIs(t, err, core.ErrNoMatch, "an agent nobody holds is the router's no match")
	assert.Nil(t, res.Addressed)
}

// A subject that is not the operator is refused before any command is read: in
// the first build every command is the operator's.
func TestACommandFromASubjectThatIsNotTheOperatorIsRefused(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	for _, text := range []string{"/pao help", "/pao stream a", "/pao agent a hi"} {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "plain", Text: text})
		require.NoError(t, err)
		assert.Equal(t, ReplyNoAccess, res.Reply, text)
	}
	assert.Empty(t, ops.calls)
	assert.Empty(t, r.routed)
}

func TestASteeringCommandWithoutOperationsSaysSo(t *testing.T) {
	l, _, _, _ := chanSetup(t)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao stream a"})
	require.NoError(t, err)
	assert.Equal(t, errNoCommands.Error(), res.Reply)
}

func TestCommandAsOperator(t *testing.T) {
	l, r, _ := setup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)

	reply, d, ok, err := l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	assert.False(t, ok, "not a command")
	assert.Nil(t, d)
	assert.Empty(t, reply)

	reply, d, ok, err = l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/pao view a resume", Target: byName("a")})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Nil(t, d)
	assert.NotEmpty(t, reply)
	assert.Equal(t, []string{"view a resume"}, ops.calls)

	reply, d, ok, err = l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/pao agent a hello"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, reply)
	require.NotNil(t, d)
	require.Len(t, r.routed, 1)
	assert.Equal(t, "[human]: hello", r.routed[0].Line())
}
