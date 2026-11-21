package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// grantsOf is a fixed set of grants per subject.
type grantsOf map[string][]access.Grant

func (g grantsOf) Of(s access.Subject) []access.Grant { return g[s.String()] }

func TestTheGovernorsDecisionsAreScopedToItsRequester(t *testing.T) {
	gov, other := access.Agent("gov"), access.Agent("bee")
	inner := access.Matrix{Grants: grantsOf{
		"operator":  {{Subject: access.Operator, Kind: access.ResourceAgent, Name: "bee"}, {Subject: access.Operator, Kind: access.ResourceAgent, Tag: "shared"}},
		"agent:gov": {{Subject: gov, Kind: access.ResourceAgent, Tag: "team"}, {Subject: gov, Kind: access.ResourceTool, Name: mcpsrv.ToolPropose}},
		"agent:bee": {{Subject: other, Kind: access.ResourceAgent, Tag: "team"}},
	}}
	serving := true
	a := governorScoped{inner: inner, governor: "gov", requester: func() (access.Subject, bool) { return access.Operator, serving }}
	bee, wasp := access.AgentResource("bee", []string{"team"}), access.AgentResource("wasp", []string{"team"})
	propose := access.ToolResource(mcpsrv.ToolPropose, nil)

	assert.True(t, a.Allow(gov, access.List, bee), "both the governor and its requester reach bee")
	assert.False(t, a.Allow(gov, access.List, wasp), "the requester does not reach wasp")
	assert.True(t, a.Allow(gov, access.CallTool, propose), "its own proposal tool reads nothing")
	assert.True(t, a.Allow(other, access.List, wasp), "another agent is not scoped")
	assert.True(t, a.Allow(access.Operator, access.List, bee), "nor the operator")
	assert.False(t, a.Allow(gov, access.List, access.AgentResource("moth", nil)), "nothing the governor's own grants do not reach")

	for _, act := range []access.Action{access.Route, access.Spawn, access.Kill, access.Lifecycle, access.Watch, access.ReadHarvest} {
		assert.False(t, a.Allow(gov, act, bee), "%s: no steering, lifecycle or other read, whatever its grants", act)
		assert.True(t, a.Allow(other, act, bee), "%s: another agent keeps what its grants give", act)
	}
	assert.False(t, a.Allow(gov, access.ListRecipes, access.AgentResource("", []string{"team"})), "a recipe the requester's grants do not reach")
	assert.True(t, a.Allow(gov, access.ListRecipes, access.AgentResource("", []string{"team", "shared"})), "a recipe both reach")
	assert.False(t, a.Allow(gov, access.CallTool, access.ToolResource("search__find", []string{"team"})), "no proxied tool")
	assert.False(t, a.Allow(gov, access.Lifecycle, propose), "the proposal tool only as a tool call")

	serving = false
	assert.False(t, a.Allow(gov, access.List, bee), "serving no one, it reads nothing")
	assert.True(t, a.Allow(gov, access.CallTool, propose))
	assert.False(t, governorScoped{inner: access.Matrix{Grants: grantsOf{}}, governor: "gov", requester: a.requester}.Allow(gov, access.CallTool, propose),
		"the proposal tool still needs the governor's own grant")
}

func TestTheGovernorServesTheOperatorWhileItsRequesterIsOne(t *testing.T) {
	flagged := map[string]bool{"alice": true}
	for name, c := range map[string]struct {
		subject, channel string
		ok, want         bool
	}{
		"an operator on a chat channel": {"alice", "chat", true, true},
		"one whose flag was taken away": {"bob", "chat", true, false},
		"the local channel":             {"human", local.Source, true, true},
		"no request":                    {"", "", false, false},
	} {
		got, ok := servedBy(func() (string, string, bool) { return c.subject, c.channel, c.ok }, func(s string) bool { return flagged[s] })()
		assert.Equal(t, c.want, ok, name)
		if ok {
			assert.Equal(t, access.Operator, got, name)
		}
	}
}
