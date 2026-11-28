package comm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// cmdOps records the operations chat commands run.
type cmdOps struct{ calls []string }

func (c *cmdOps) SetStream(_ context.Context, agent, channel string, _ int) error {
	c.calls = append(c.calls, "stream "+agent+" "+channel)
	return nil
}

func (c *cmdOps) Bind(_ context.Context, b command.Binding) (command.Binding, error) {
	c.calls = append(c.calls, "bind "+b.Agent+" "+b.To)
	return b, nil
}

func (c *cmdOps) Unbind(_ context.Context, b command.Binding) (command.Binding, error) {
	c.calls = append(c.calls, "unbind "+b.Agent+" "+b.To)
	return b, nil
}

func (c *cmdOps) Bindings(context.Context, string) ([]command.Binding, error) { return nil, nil }

func (c *cmdOps) View(_ context.Context, agent, action string) error {
	c.calls = append(c.calls, "view "+agent+" "+action)
	return nil
}

func (c *cmdOps) Agents(context.Context) ([]command.Agent, error) {
	c.calls = append(c.calls, "agents")
	return []command.Agent{{Name: "a", State: "running"}}, nil
}

func (c *cmdOps) Spawn(_ context.Context, recipe, name, channel string) (string, error) {
	c.calls = append(c.calls, "spawn "+recipe+" "+channel)
	return recipe + "-1", nil
}

func (c *cmdOps) Kill(_ context.Context, name string) (command.Killed, error) {
	c.calls = append(c.calls, "kill "+name)
	return command.Killed{Harvest: "h1"}, nil
}
func (c *cmdOps) File(_ context.Context, agent, path string) (command.File, error) {
	return command.File{Name: path, Data: []byte(agent)}, nil
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
	for _, text := range []string{"/pao stream a", "/pao help", "/pao nosuch", "/pao", "/pao view a pause", "/pao allow q1", "/pao agents"} {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: text, Target: byName("a")})
		require.NoError(t, err, text)
		assert.Nil(t, res.Delivery, text)
		assert.Nil(t, res.Addressed, text)
		assert.NotEmpty(t, res.Reply, text)
	}
	assert.Empty(t, r.routed, "the sticky target does not carry a command to an agent")
	assert.Equal(t, []string{"stream a chat", "view a pause", "allow q1", "agents"}, ops.calls, "a stream with no channel named is bound to the one the command came on")
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
// the first build every command is the operator's. One that is not the
// channel's owner is told nothing at all.
func TestACommandFromASubjectThatIsNotTheOperatorIsRefused(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	for _, owner := range []string{"alice", "bob"} {
		l.SetScopes(map[string]Scope{"chat": {Owner: owner}})
		want := ReplyNoAccess
		if owner == "alice" {
			want = ""
		}
		for _, text := range []string{"/pao help", "/pao stream a", "/pao agent a hi", "/pao agents"} {
			res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "plain", Text: text})
			require.NoError(t, err)
			assert.Equal(t, want, res.Reply, owner+": "+text)
		}
	}
	assert.Empty(t, ops.calls)
	assert.Empty(t, r.routed)
}

func TestASteeringCommandWithoutOperationsSaysSo(t *testing.T) {
	l, _, _, _ := chanSetup(t)
	for _, text := range []string{"/pao stream a", "/pao agents"} {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: text})
		require.NoError(t, err)
		assert.Equal(t, errNoCommands.Error(), res.Reply, text)
	}
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

// buttonChannel shows actions and records what it is asked to close.
type buttonChannel struct {
	fakeChannel
	closed []string
	err    error
}

func (c *buttonChannel) CloseActions(_ context.Context, id, outcome string) error {
	c.closed = append(c.closed, id+" "+outcome)
	return c.err
}

func TestSettledActionsAreClosedOnEveryChannelThatShowsButtons(t *testing.T) {
	l, _, _ := setup(t)
	plain := &fakeChannel{name: "local"}
	one, two := &buttonChannel{fakeChannel: fakeChannel{name: "chat"}}, &buttonChannel{fakeChannel: fakeChannel{name: "other"}}
	l.AddChannel(plain)
	l.AddChannel(one)
	l.AddChannel(two)

	m, err := l.notify(context.Background(), core.Message{Sender: "pao", Text: "may I?", Actions: []core.Action{{Label: "ALLOW", Command: "/pao allow q1"}}})
	require.NoError(t, err)
	for _, c := range []*fakeChannel{plain, &one.fakeChannel, &two.fakeChannel} {
		require.Len(t, c.got, 1)
		assert.Equal(t, []core.Action{{Label: "ALLOW", Command: "/pao allow q1"}}, c.got[0].Actions, "every channel gets the actions; one without buttons ignores them")
	}

	two.err = errors.New("gone")
	err = l.CloseActions(context.Background(), m.ID, "allowed")
	assert.ErrorContains(t, err, `channel "other": gone`)
	assert.Equal(t, []string{m.ID + " allowed"}, one.closed)
	assert.Equal(t, []string{m.ID + " allowed"}, two.closed, "a failure on one channel does not stop the others")
}

// A press is only ever a command. One whose payload is anything else is refused,
// recorded and never routed, so a forged payload cannot reach an agent as text.
func TestAPressThatIsNotACommandIsRefusedAndNeverRouted(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hello agent", Raw: "hello agent", Press: true, Target: byName("a")})
	require.NoError(t, err)
	assert.Equal(t, ReplyNotACommand, res.Reply)
	assert.Nil(t, res.Delivery)
	assert.Empty(t, r.routed)
	assert.Empty(t, ops.calls)
	assert.Equal(t, []rec{{"chat", "op", RecordMessage, "press not a command", "hello agent"}}, rc.got)

	// The same text typed is routed as ever.
	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "hello agent", Target: byName("a")})
	require.NoError(t, err)
	assert.NotNil(t, res.Delivery)
}

// A press is the command it carries, from whoever pressed, under the same checks.
func TestAPressIsTheTypedCommandFromThePresser(t *testing.T) {
	l, r, _, rc := chanSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao allow q1", Press: true})
	require.NoError(t, err)
	assert.Equal(t, "q1: allow", res.Reply)
	assert.Equal(t, []string{"allow q1"}, ops.calls)

	for _, sender := range []string{"plain", "stranger"} {
		res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: sender, Text: "/pao allow q1", Press: true})
		require.NoError(t, err)
		assert.NotEqual(t, "q1: allow", res.Reply, sender)
	}
	res, err = l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao allow q1", Press: true, Group: true})
	require.NoError(t, err)
	assert.Equal(t, ChannelResult{}, res, "a press in a group is a group's message")
	assert.Equal(t, []string{"allow q1"}, ops.calls, "only the operator's own press decided")
	assert.Empty(t, r.routed)
	assert.Len(t, rc.got, 3, "the refused presses are recorded")
}

// A command that sends a file hands it to the adapter for the sender alone
// (ADR-0052 point 1).
func TestAFileCommandsFileReachesTheAdapter(t *testing.T) {
	l, r, _, _ := chanSetup(t)
	l.SetCommands(&cmdOps{})
	res, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: "op", Text: "/pao file a notes/x.md"})
	require.NoError(t, err)
	require.NotNil(t, res.File)
	assert.Equal(t, "notes/x.md", res.File.Name)
	assert.Equal(t, "a", string(res.File.Data))
	assert.Empty(t, r.routed)
}
