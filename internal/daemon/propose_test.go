package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// proposeOps is mcpOps over a layer with the local channel, where the operator
// has just written to agent a.
func proposeOps(t *testing.T) (mcpOps, *recChannel) {
	t.Helper()
	ops, _ := sendOps(t, nil)
	local := &recChannel{name: "local"}
	ops.layer.AddChannel(local)
	_, err := ops.layer.ReceiveAsOperator(context.Background(), comm.Inbound{Source: "local", Sender: "alice", Text: "pause coder", Target: core.Target{Kind: core.ByName, Value: "a"}})
	require.NoError(t, err)
	return ops, local
}

func TestAGovernorsProposalIsShownWithAButtonPaoDrawsFromTheCommand(t *testing.T) {
	ops, local := proposeOps(t)
	require.NoError(t, ops.Propose(context.Background(), access.Agent("a"), "  /pao view coder pause "))
	require.Len(t, local.got, 1)
	m := local.got[0]
	assert.Equal(t, "pao", m.Sender)
	assert.Equal(t, core.MessageSystemEvent, m.Kind)
	assert.Equal(t, "a proposes:\n/pao view coder pause\nsend it, or press the button, to run it.", m.Text)
	assert.Equal(t, []core.Action{{Label: "/pao view coder pause", Command: "/pao view coder pause"}}, m.Actions,
		"the button is labelled with exactly what it runs")
}

func TestAProposalThatIsNotOneProposableCommandIsRefused(t *testing.T) {
	ops, local := proposeOps(t)
	for _, text := range []string{"/pao allow q1", "/pao help", "view coder pause", "/pao agents\n/pao deny q1"} {
		assert.Error(t, ops.Propose(context.Background(), access.Agent("a"), text), "%q", text)
	}
	assert.Empty(t, local.got, "nothing is shown")
}

func TestANodeThatNarrowsWhatTheGovernorProposesNarrowsEveryProposal(t *testing.T) {
	ops, local := proposeOps(t)
	ops.propose = []string{"agents", "view"}
	err := ops.Propose(context.Background(), access.Agent("a"), "/pao spawn helper")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"spawn" is not a command a governor may propose`)
	assert.Empty(t, local.got)
	require.NoError(t, ops.Propose(context.Background(), access.Agent("a"), "/pao view coder pause"))
	assert.Len(t, local.got, 1)
}
