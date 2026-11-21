package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func askMsg(text string) core.Message {
	return core.Message{Sender: "pao", Text: text, Actions: []core.Action{{Label: "ALLOW", Command: "/pao allow q1"}}}
}

func noticeMsg(text string) core.Message { return core.Message{Sender: "pao", Text: text} }

func stderrTo(agent, to string) Binding {
	kind, name, _ := cut(to)
	return Binding{Agent: agent, Stream: StreamStderr, To: Dest{Kind: kind, Name: name}}
}

func notifyAgent(t *testing.T, l *Layer, m core.Message) core.Message {
	t.Helper()
	stored, err := l.NotifyAgent(context.Background(), "a", m)
	require.NoError(t, err)
	return stored
}

func TestWithNoStderrBindingPaosMessagesAboutAnAgentGoToEachOperatorPrivately(t *testing.T) {
	l, _, chat, local := originSetup(t)
	notifyAgent(t, l, askMsg("q"))
	notifyAgent(t, l, noticeMsg("n"))
	assert.Equal(t, map[string][]string{"op": {"q", "n"}}, chat.take(), "a question and a notice alike, never the admitted group")
	assert.Equal(t, []string{"q", "n"}, local.texts(), "and the local channel")
}

func TestAStderrBindingTakesPaosMessagesAboutTheAgentInPlaceOfTheDefault(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{stderrTo("a", "group:team")})
	notifyAgent(t, l, askMsg("q"))
	notifyAgent(t, l, noticeMsg("n"))
	assert.Equal(t, map[string][]string{"-5": {"q", "n"}}, chat.take(), "the group only, not the operator privately")
	assert.Empty(t, local.texts(), "nor a channel the binding does not name")

	l.SetBindings([]Binding{stderrTo("a", "channel:local")})
	notifyAgent(t, l, askMsg("q2"))
	assert.Empty(t, chat.take())
	assert.Equal(t, []string{"q2"}, local.texts())
}

func TestAnotherAgentsStderrBindingIsNotThisOnes(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{stderrTo("b", "group:team")})
	notifyAgent(t, l, askMsg("q"))
	assert.Equal(t, map[string][]string{"op": {"q"}}, chat.take())
}

func TestStdoutAndStderrBindingsCarryTheirOwnStreamOnly(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", 2, ""), stderrTo("a", "private:alice")})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"x@1"}, "-5": {"x@2"}}, chat.take(), "the stderr binding adds no copy of the output")
	notifyAgent(t, l, askMsg("q"))
	assert.Equal(t, map[string][]string{"op": {"q"}}, chat.take(), "the stdout binding carries no question")
}

func TestAConversationTwoStderrBindingsNameIsShownOneCopy(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{stderrTo("a", "private:alice"), {Agent: "a", Stream: StreamStderr, To: Dest{Kind: DestPrivate, Name: "alice", Channel: "chat"}}})
	notifyAgent(t, l, askMsg("q"))
	assert.Equal(t, map[string][]string{"op": {"q"}}, chat.take())
}

func TestAStderrDestinationAReaderMayNotSeeIsHeldBackNotReplaced(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{stderrTo("a", "private:bob"), stderrTo("a", "group:team")})
	notifyAgent(t, l, askMsg("q"))
	assert.Equal(t, map[string][]string{"-5": {"q"}}, chat.take(), "bob is not an operator: his conversation is held back, the group still shown")
	assert.Empty(t, local.texts())

	l.SetBindings([]Binding{stderrTo("a", "private:bob")})
	_, err := l.NotifyAgent(context.Background(), "a", askMsg("q2"))
	require.Error(t, err, "a question nobody is shown cannot be answered")
	assert.Contains(t, err.Error(), `agent "a"`)
	assert.Empty(t, chat.take(), "never sent to the default in its place")
	assert.Empty(t, local.texts())
}

func TestAStderrBindingThatNamesNoConversationIsAnError(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{stderrTo("a", "group:gone")})
	_, err := l.NotifyAgent(context.Background(), "a", noticeMsg("n"))
	require.Error(t, err)
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestAGroupWhosePolicyIsUnknownIsNotShownPaosMessage(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetGroups(map[string]map[string]Group{"chat": {"-5": {Name: "team", Policy: "everyone"}}})
	l.SetBindings([]Binding{stderrTo("a", "group:team")})
	_, err := l.NotifyAgent(context.Background(), "a", askMsg("q"))
	require.Error(t, err)
	assert.Empty(t, chat.take())
}

func TestPaosMessageAboutAnAgentIsInNoAgentsScope(t *testing.T) {
	l, _, _, _ := originSetup(t)
	stored := notifyAgent(t, l, noticeMsg("secret pane"))
	assert.Equal(t, core.MessageSystemEvent, stored.Kind)
	_, err := l.store.Resolve("a", "secret pane")
	assert.Error(t, err, "the agent cannot quote it")
	_, err = l.Fetch("a", stored.ID)
	assert.Error(t, err, "nor fetch it")
}
