package comm

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

var (
	output   = core.Message{Sender: "a", Kind: core.MessageContent, Text: "done"}
	notice   = core.Message{Sender: "pao", Kind: core.MessageSystemEvent, Text: "agent a stopped"}
	question = core.Message{Sender: "pao", Kind: core.MessageSystemEvent, Text: "allow?", Actions: []core.Action{{Label: "Allow", Command: "/pao allow q1"}}}
)

func TestWithAnAdmittedGroupOutputGoesToTheGroupAndAQuestionToTheGroupAndEachOperator(t *testing.T) {
	l, _, id, _ := groupSetup(t)
	id.bound["chat\x00op2"] = "carol"
	id.operators["carol"] = true

	assert.Equal(t, []string{"-5"}, l.Destinations("chat", output), "an agent's output: the group only")
	assert.Equal(t, []string{"-5"}, l.Destinations("chat", notice), "pao's other notices are output too")
	got := l.Destinations("chat", question)
	assert.Equal(t, "-5", got[0], "a question: the group")
	assert.ElementsMatch(t, []string{"-5", "op", "op2"}, got, "and each operator privately")
}

func TestEveryAdmittedGroupOfTheChannelGetsItInOrder(t *testing.T) {
	l, _, _, _ := chanSetup(t)
	l.SetGroups(map[string]map[string]string{"chat": {"-7": PolicyOperators, "-5": PolicyOperators}, "other": {"-9": PolicyOperators}})
	assert.Equal(t, []string{"-5", "-7"}, l.Destinations("chat", output))
	assert.Equal(t, []string{"-5", "-7", "op"}, l.Destinations("chat", question))
}

func TestAChannelWithNoAdmittedGroupSendsPrivatelyAsBefore(t *testing.T) {
	l, _, _, _ := groupSetup(t)
	l.SetGroups(map[string]map[string]string{"other": {"-9": PolicyOperators}})
	for _, m := range []core.Message{output, notice, question} {
		assert.Equal(t, []string{"op"}, l.Destinations("chat", m), m.Text)
	}
}

func TestAGroupUnderAPolicyTheLayerDoesNotKnowIsShownNothing(t *testing.T) {
	l, _, _, _ := chanSetup(t)
	l.SetGroups(map[string]map[string]string{"chat": {"-5": "everyone", "": PolicyOperators}})
	assert.Equal(t, []string{"op"}, l.Destinations("chat", output), "and so the channel sends privately, as with no group")
	assert.Equal(t, []string{"op"}, l.Destinations("chat", question))
}

// The narrowest-reader rule (ADR-0028 point 5) holds in a private conversation
// too: its one reader is the operator, who sees an agent's output only if the
// operator may reach the agent.
func TestOutputOfAnAgentTheOperatorMayNotReachIsShownInNoConversation(t *testing.T) {
	for name, set := range map[string]func(*testing.T) (*Layer, *fakeRouter, *fakeIdentity, *fakeRecorder){"with a group": groupSetup, "private only": chanSetup} {
		l, r, _, _ := set(t)
		r.agents["b"] = []string{"b"}
		l.SetOperatorAccess(func(p core.Profile) bool { return p.Name != "a" })
		assert.Empty(t, l.Destinations("chat", output), name)
		assert.NotEmpty(t, l.Destinations("chat", core.Message{Sender: "b", Kind: core.MessageContent, Text: "x"}), "%s: an agent the operator may reach", name)
		assert.NotEmpty(t, l.Destinations("chat", question), "%s: a question is the operator's to answer", name)
		assert.NotEmpty(t, l.Destinations("chat", notice), "%s: pao's own notice is the operator's", name)
	}
}

func TestOutputFromASenderPaoCannotNameIsShownNowhereOnceAccessIsSet(t *testing.T) {
	l, _, _, _ := groupSetup(t)
	ghost := core.Message{Sender: "ghost", Kind: core.MessageContent, Text: "boo"}
	assert.Equal(t, []string{"-5"}, l.Destinations("chat", ghost), "no access set: nothing is narrowed")
	l.SetOperatorAccess(func(core.Profile) bool { return true })
	assert.Empty(t, l.Destinations("chat", ghost), "with access set, an agent that cannot be resolved cannot be shown to be reachable")
}

func TestOnlyPaosOwnMessageWithActionsIsAQuestion(t *testing.T) {
	l, _, _, _ := groupSetup(t)
	withActions := output
	withActions.Actions = question.Actions
	assert.Equal(t, []string{"-5"}, l.Destinations("chat", withActions), "an agent's message is output, whatever it carries")
}
