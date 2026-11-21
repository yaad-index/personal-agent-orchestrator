package comm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestTellGoesToTheSubjectAloneAndPrivately(t *testing.T) {
	l, _, chat, local := originSetup(t)
	m, err := l.Tell(context.Background(), "chat", "alice", core.Message{Sender: "pao", Text: "ready"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"ready"}}, chat.take(), "alice's private conversation: never the group")
	assert.Empty(t, local.texts(), "nor another channel")
	assert.Equal(t, core.MessageSystemEvent, m.Kind)
	_, err = l.Fetch("a", m.ID)
	assert.Error(t, err, "in no agent's scope")
}

func TestTellOnTheLocalChannelGoesToItsOneConversation(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.Tell(context.Background(), "local", "", core.Message{Sender: "pao", Text: "ready"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ready"}, local.texts())
	assert.Empty(t, chat.take())
}

func TestTellWithNoPrivateConversationSendsNothing(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.Tell(context.Background(), "chat", "bob", core.Message{Sender: "pao", Text: "ready"})
	require.Error(t, err, "bob is not an operator, so his conversation is not shown it")
	_, err = l.Tell(context.Background(), "chat", "nobody", core.Message{Sender: "pao", Text: "ready"})
	require.Error(t, err, "no conversation at all")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestASpawnFromAGroupIsAnsweredThereAndToldItsOutcomePrivately(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	ops := &cmdOps{}
	l.SetCommands(ops)
	res, err := l.ReceiveFromChannel(context.Background(), inGroup("-5", "op", "/pao spawn helper"))
	require.NoError(t, err)
	assert.Contains(t, res.Reply, "spawning an agent from recipe helper")
	var got map[string][]string
	require.Eventually(t, func() bool {
		got = chat.take()
		return len(got) > 0
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, map[string][]string{"op": {"helper-1 is ready, spawned from recipe helper"}}, got, "to the sender privately, not to the group")
	assert.Equal(t, []string{"spawn helper chat"}, ops.calls)
	assert.Empty(t, r.routed)
}

func TestASpawnFromTheLocalChannelIsToldItsOutcomeThere(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetCommands(&cmdOps{})
	reply, _, ok, err := l.CommandAsOperator(context.Background(), Inbound{Source: "local", Sender: "human", Text: "/pao spawn helper"})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, reply, "spawning")
	require.Eventually(t, func() bool { return len(local.texts()) > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"helper-1 is ready, spawned from recipe helper"}, local.texts())
	assert.Empty(t, chat.take())
}
