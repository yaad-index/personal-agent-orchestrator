package comm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func proposal(cmd string) core.Message {
	return core.Message{Sender: "pao", Text: cmd, Actions: []core.Action{{Label: cmd, Command: cmd}}}
}

func propose(t *testing.T, l *Layer, cmd string) core.Message {
	t.Helper()
	m, err := l.Propose(context.Background(), "a", proposal(cmd))
	require.NoError(t, err)
	return m
}

func TestAProposalGoesToTheRequesterPrivatelyWhereverTheTurnBegan(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, privately("bind it"))
	propose(t, l, "/pao agents")
	assert.Equal(t, map[string][]string{"op": {"/pao agents"}}, chat.take())

	fromChat(t, l, inGroup("-5", "op", "bind it"))
	propose(t, l, "/pao view a pause")
	assert.Equal(t, map[string][]string{"op": {"/pao view a pause"}}, chat.take(), "asked in the group, proposed privately: never to the group")
	assert.Empty(t, local.texts())
}

func TestAProposalInATurnFromTheLocalChannelGoesThere(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	propose(t, l, "/pao agents")
	assert.Equal(t, []string{"/pao agents"}, local.texts())
	assert.Empty(t, chat.take())
}

func TestAProposalIsInNoAgentsScopeAndCarriesItsButton(t *testing.T) {
	l, _, _, local := originSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	m := propose(t, l, "/pao agents")
	assert.Equal(t, core.MessageSystemEvent, m.Kind)
	_, err = l.Fetch("a", m.ID)
	assert.Error(t, err, "the agent cannot fetch what was shown to the operator")
	require.Len(t, local.got, 1)
	assert.Equal(t, []core.Action{{Label: "/pao agents", Command: "/pao agents"}}, local.got[0].Actions)
}

func TestWithNoOneToProposeToNothingIsSent(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "no turn at all")

	_, err = l.ReceiveAsOperator(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "a turn another agent started")
	assert.Contains(t, err.Error(), "no one to propose to")

	_, err = l.ReceiveAsOperator(context.Background(), Inbound{Source: "chat", Sender: "bob", Conversation: "plain", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "bob is not an operator, so his conversation is not shown it")
	assert.Contains(t, err.Error(), "bob")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestAFailedDeliveryPutsBackTheRequesterWithTheOrigin(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	fromChat(t, l, privately("first"))
	r.fail = map[string]error{"a": errors.New("down")}
	_, _ = l.ReceiveAsOperator(context.Background(), Inbound{Source: "chat", Sender: "bob", Conversation: "plain", Text: "hi", Target: byName("a")})
	r.fail = nil
	propose(t, l, "/pao agents")
	assert.Equal(t, map[string][]string{"op": {"/pao agents"}}, chat.take(), "bob's message never reached the agent, so alice's turn stands")
}
