package comm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestTheLogLevelMirrorsADeliveredMessageWithItsSender(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
	fromChat(t, l, privately("hi"))
	assert.Equal(t, map[string][]string{"-5": {"hi"}}, chat.take(), "the group is shown what the agent was sent; the conversation it came from is not shown it again")
	entry(t, l, atLevel("x", core.LevelLog))
	assert.Equal(t, map[string][]string{"op": {"x@1"}, "-5": {"x@2147483647"}}, chat.take(), "and the agent's output at every level")
}

func TestAMirroredMessageNamesItsSender(t *testing.T) {
	l, _, _, local := originSetup(t)
	l.SetBindings([]Binding{bind("channel:local", core.LevelLog, "")})
	fromChat(t, l, privately("hi"))
	require.Len(t, local.got, 1)
	assert.Equal(t, "[alice]: hi", local.got[0].Shown())
}

func TestTheConversationAMessageCameFromIsNotShownItAgain(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
	fromChat(t, l, inGroup("-5", "op", "hi"))
	assert.Empty(t, chat.take())
}

func TestALevelBelowLogMirrorsNothing(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", 4, ""), bind("channel:local", core.LevelLog-1, "")})
	fromChat(t, l, privately("hi"))
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestADestinationFollowingSeveralAgentsIsShownAMessageOnce(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, ""), {Agent: "b", To: Dest{Kind: DestGroup, Name: "team"}, Level: core.LevelLog}})
	fromChat(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "to both", Target: core.Target{Kind: core.ByTag, Value: "team"}})
	assert.Equal(t, map[string][]string{"-5": {"to both"}}, chat.take())
}

// A message is mirrored before it is delivered, so the agent's answer never comes
// before it.
func TestAMirroredMessageComesBeforeTheAnswer(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
	r.atRoute = func(m core.Message) {
		if m.Text == "hi" {
			entry(t, l, atLevel("answer", core.LevelLog))
		}
	}
	fromChat(t, l, privately("hi"))
	assert.Equal(t, map[string][]string{"-5": {"hi", "answer@2147483647"}, "op": {"answer@1"}}, chat.take())
}

// The cost of mirroring first: a delivery that then fails was still shown.
func TestAMessageWhoseDeliveryFailsWasStillMirrored(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
	r.fail = map[string]error{"a": errors.New("down")}
	fromChat(t, l, privately("lost"))
	assert.Equal(t, map[string][]string{"-5": {"lost"}}, chat.take())
}

func TestAMirroredMessageIsHeldBackWhereAReaderMayNotReachTheAgent(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	r.agents["b"] = []string{"b"}
	l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
	l.SetOperatorAccess(func(p core.Profile) bool { return p.Name != "a" })
	_, err := l.Receive(context.Background(), Inbound{Source: "mcp", Sender: "someone", Text: "hi", Target: byName("a")})
	require.NoError(t, err, "delivered: the source does not route as the operator")
	assert.Empty(t, chat.take(), "but the group's readers may not reach the agent")
}

// A message from another agent is shown only where every reader may reach both
// agents.
func TestAMessageFromAnotherAgentIsMirroredOnlyWhereItsSenderMayBeReachedToo(t *testing.T) {
	for name, c := range map[string]struct {
		allow func(core.Profile) bool
		want  map[string][]string
	}{
		"both reachable":     {func(core.Profile) bool { return true }, map[string][]string{"-5": {"from b"}}},
		"sender unreachable": {func(p core.Profile) bool { return p.Name != "b" }, map[string][]string{}},
	} {
		l, r, chat, _ := originSetup(t)
		r.agents["b"] = []string{"b"}
		l.SetBindings([]Binding{bind("group:team", core.LevelLog, "")})
		l.SetOperatorAccess(c.allow)
		_, err := l.Receive(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "from b", Target: byName("a")})
		require.NoError(t, err)
		assert.Equal(t, c.want, chat.take(), name)
	}
}
