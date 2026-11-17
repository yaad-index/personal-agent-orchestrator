package access

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allActions = []Action{List, ListRecipes, Route, Spawn, Kill, ReadHarvest, Watch}

func TestOperatorOnlyAllowsTheOperatorAndNoOneElse(t *testing.T) {
	var a Authorizer = OperatorOnly{}
	r := AgentResource("t", []string{"x"})
	for _, act := range allActions {
		assert.True(t, a.Allow(Operator, act, r), "operator %s", act)
		assert.False(t, a.Allow(Agent("other"), act, r), "agent %s", act)
		assert.False(t, a.Allow(Agent("t"), act, r), "an agent on itself %s", act)
	}
}

func TestTheZeroSubjectIsNobody(t *testing.T) {
	assert.False(t, OperatorOnly{}.Allow(Subject{}, Kill, Resource{}))
	assert.False(t, OperatorOnly{}.Allow(Subject{Name: "operator"}, Kill, Resource{}), "a name does not make an operator")
	assert.False(t, Matrix{Grants: grantList{{Subject: Subject{}, Kind: ResourceAgent, Tag: "x"}}}.Allow(Subject{}, Kill, AgentResource("a", []string{"x"})))
}

func TestNilAuthorizerIsClosed(t *testing.T) {
	assert.True(t, Allowed(nil, Operator, Kill, Resource{}))
	assert.False(t, Allowed(nil, Agent("a"), Kill, Resource{}), "no authorizer means agents are denied, not allowed")
}

type allowAll struct{}

func (allowAll) Allow(Subject, Action, Resource) bool { return true }

func TestAllowedUsesTheGivenAuthorizer(t *testing.T) {
	assert.True(t, Allowed(allowAll{}, Agent("a"), ReadHarvest, Resource{}))
}

type grantList []Grant

func (l grantList) Of(s Subject) []Grant {
	var out []Grant
	for _, g := range l {
		if g.Subject == s {
			out = append(out, g)
		}
	}
	return out
}

func TestAnAgentReachesOnlyWhatACellOrATagScopeGrantsIt(t *testing.T) {
	m := Matrix{Grants: grantList{
		{Subject: Agent("a"), Kind: ResourceAgent, Name: "b"},
		{Subject: Agent("a"), Kind: ResourceAgent, Tag: "team"},
		{Subject: Agent("z"), Kind: ResourceAgent, Name: "secret"},
	}}
	for _, act := range allActions {
		assert.True(t, m.Allow(Agent("a"), act, AgentResource("b", nil)), "a cell, %s", act)
		assert.True(t, m.Allow(Agent("a"), act, AgentResource("c", []string{"x", "team"})), "a tag scope, %s", act)
		assert.False(t, m.Allow(Agent("a"), act, AgentResource("d", []string{"x"})), "nothing grants d, %s", act)
		assert.False(t, m.Allow(Agent("a"), act, AgentResource("secret", nil)), "another subject's grant is not a's, %s", act)
		assert.False(t, m.Allow(Agent("a"), act, AgentResource("a", nil)), "not even itself without a grant, %s", act)
		assert.False(t, m.Allow(Agent("b"), act, AgentResource("b", nil)), "a grant is one way, %s", act)
	}
}

func TestACellNeedsANamedResourceAndAKindMatches(t *testing.T) {
	m := Matrix{Grants: grantList{
		{Subject: Agent("a"), Kind: ResourceAgent, Name: "b"},
		{Subject: Agent("a"), Kind: "tool", Tag: "team"},
	}}
	assert.False(t, m.Allow(Agent("a"), ListRecipes, AgentResource("", []string{"team"})), "a cell does not cover an unnamed resource, and a tool scope is not an agent scope")
	assert.False(t, m.Allow(Agent("a"), Route, Resource{Kind: "tool", Name: "b"}), "an agent cell is not a tool cell")
}

func TestWithoutGrantsAnAgentIsDeniedAndTheOperatorIsNot(t *testing.T) {
	for _, m := range []Matrix{{}, {Grants: grantList{}}} {
		for _, act := range allActions {
			assert.False(t, m.Allow(Agent("a"), act, AgentResource("b", []string{"t"})))
			assert.True(t, m.Allow(Operator, act, AgentResource("b", []string{"t"})), "the operator is not behind grants yet")
		}
	}
}

func TestSubjectsRoundTripAndBadOnesAreRefused(t *testing.T) {
	for _, s := range []Subject{Operator, Agent("bee")} {
		got, err := ParseSubject(s.String())
		require.NoError(t, err)
		assert.Equal(t, s, got)
	}
	for _, bad := range []string{"", "agent:", "bee", "agent:a b", "agent:a:b", "Operator"} {
		_, err := ParseSubject(bad)
		assert.Error(t, err, bad)
	}
}

func TestAGrantNamesOneResourceOrOneTag(t *testing.T) {
	ok := Grant{Subject: Agent("a"), Kind: ResourceAgent, Name: "b"}
	assert.NoError(t, ok.Validate())
	for name, g := range map[string]Grant{
		"both":       {Subject: Agent("a"), Kind: ResourceAgent, Name: "b", Tag: "t"},
		"neither":    {Subject: Agent("a"), Kind: ResourceAgent},
		"no subject": {Kind: ResourceAgent, Name: "b"},
		"no name":    {Subject: Agent(""), Kind: ResourceAgent, Name: "b"},
		"tool kind":  {Subject: Agent("a"), Kind: "tool", Name: "b"},
		"space":      {Subject: Agent("a"), Kind: ResourceAgent, Tag: "a b"},
	} {
		assert.Error(t, g.Validate(), name)
	}
}
