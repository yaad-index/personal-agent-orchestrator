package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestANodeNoticeGoesToEachOperatorPrivatelyNeverToAGroup(t *testing.T) {
	l, _, chat, local := originSetup(t)
	// A group was spoken in, so it is a conversation the chat channel knows.
	fromChat(t, l, inGroup("-5", "op", "hi"))
	chat.take()
	local.texts()

	_, err := l.NotifyOperators(context.Background(), core.Message{Sender: "pao", Text: "budget: account x cannot be read"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"budget: account x cannot be read"}}, chat.take(), "the operator privately, not the group")
	assert.Equal(t, []string{"budget: account x cannot be read"}, local.texts(), "and the operator on the local channel")

	// Notify, by contrast, goes to every conversation, the group among them.
	_, err = l.notify(context.Background(), core.Message{Sender: "pao", Text: "to all"})
	require.NoError(t, err)
	assert.Contains(t, chat.take(), "-5")
}

func TestANoticeOnOneChannelGoesToItsOperatorsPrivatelyAndNowhereElse(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, inGroup("-5", "op", "hi"))
	chat.take()
	local.texts()

	_, err := l.NotifyOperatorsOn(context.Background(), "chat", core.Message{Sender: "pao", Text: "view paused"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"view paused"}}, chat.take(), "the operator privately, not the group")
	assert.Empty(t, local.texts(), "and on no other channel")

	_, err = l.NotifyOperatorsOn(context.Background(), "missing", core.Message{Sender: "pao", Text: "lost"})
	assert.Error(t, err, "a channel that is not registered is an error, not every channel")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}
