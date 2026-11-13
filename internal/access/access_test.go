package access

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOperatorOnlyAllowsTheOperatorAndNoOneElse(t *testing.T) {
	var a Authorizer = OperatorOnly{}
	r := Resource{Agent: "t", Tags: []string{"x"}}
	for _, act := range []Action{List, ListRecipes, Route, Spawn, Kill, ReadHarvest} {
		assert.True(t, a.Allow(Operator, act, r), "operator %s", act)
		assert.False(t, a.Allow(Agent("other"), act, r), "agent %s", act)
		assert.False(t, a.Allow(Agent("t"), act, r), "an agent on itself %s", act)
	}
}

func TestTheZeroSubjectIsNobody(t *testing.T) {
	assert.False(t, OperatorOnly{}.Allow(Subject{}, Kill, Resource{}))
	assert.False(t, OperatorOnly{}.Allow(Subject{Name: "operator"}, Kill, Resource{}), "a name does not make an operator")
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
