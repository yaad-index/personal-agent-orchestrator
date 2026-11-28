package daemon

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// grantsOf is a fixed set of grants per subject.
type grantsOf map[string][]access.Grant

func (g grantsOf) Of(s access.Subject) []access.Grant { return g[s.String()] }

func TestTheGovernorReadsUnderItsOwnGrantsAndActsOnNothing(t *testing.T) {
	gov, other := access.Agent("gov"), access.Agent("bee")
	inner := access.Matrix{Grants: grantsOf{
		"operator":  {{Subject: access.Operator, Kind: access.ResourceAgent, Name: "bee"}},
		"agent:gov": {{Subject: gov, Kind: access.ResourceAgent, Tag: "team"}, {Subject: gov, Kind: access.ResourceTool, Name: mcpsrv.ToolPropose}},
		"agent:bee": {{Subject: other, Kind: access.ResourceAgent, Tag: "team"}},
	}}
	serving := true
	a := governorScoped{inner: inner, governor: "gov", serving: func() bool { return serving }}
	bee, wasp := access.AgentResource("bee", []string{"team"}), access.AgentResource("wasp", []string{"team"})
	propose := access.ToolResource(mcpsrv.ToolPropose, nil)

	assert.True(t, a.Allow(gov, access.List, bee))
	assert.True(t, a.Allow(gov, access.List, wasp), "the requester does not reach wasp, but the governor's own grants do")
	assert.True(t, a.Allow(gov, access.ListRecipes, access.AgentResource("", []string{"team"})), "a recipe the requester's grants do not reach")
	assert.True(t, a.Allow(gov, access.CallTool, propose), "its own proposal tool reads nothing")
	assert.True(t, a.Allow(other, access.List, wasp), "another agent is not scoped")
	assert.True(t, a.Allow(access.Operator, access.List, bee), "nor the operator")
	assert.False(t, a.Allow(gov, access.List, access.AgentResource("moth", nil)), "nothing the governor's own grants do not reach")
	assert.False(t, a.Allow(gov, access.ListRecipes, access.AgentResource("", []string{"other"})))

	for _, act := range []access.Action{access.Route, access.Spawn, access.Kill, access.Lifecycle, access.Watch, access.ReadHarvest} {
		assert.False(t, a.Allow(gov, act, bee), "%s: no steering, lifecycle or other read, whatever its grants", act)
		assert.True(t, a.Allow(other, act, bee), "%s: another agent keeps what its grants give", act)
	}
	assert.False(t, a.Allow(gov, access.CallTool, access.ToolResource("search__find", []string{"team"})), "no proxied tool")
	assert.False(t, a.Allow(gov, access.Lifecycle, propose), "the proposal tool only as a tool call")

	serving = false
	assert.False(t, a.Allow(gov, access.List, bee), "serving no one, it reads nothing")
	assert.False(t, a.Allow(gov, access.ListRecipes, access.AgentResource("", []string{"team"})))
	assert.True(t, a.Allow(gov, access.CallTool, propose))
	assert.False(t, governorScoped{inner: access.Matrix{Grants: grantsOf{}}, governor: "gov", serving: a.serving}.Allow(gov, access.CallTool, propose),
		"the proposal tool still needs the governor's own grant")
}

func TestTheGovernorIsToldTheChannelsByNameAndShapeAndNoSecret(t *testing.T) {
	provs := []config.Provider{
		{Name: "tg", Kind: "bot", CredentialEnv: "CHAT_SECRET_VAR", Options: map[string]any{"endpoint": "https://hidden.example", "token": "s3cr3t-value"}},
		{Name: "tg2", Kind: "bot", CredentialEnv: "SIDE_SECRET_VAR"},
	}
	chans := []config.Channel{{
		ID: 1, Name: "chat", Provider: "tg",
		Groups: []config.Group{{ID: "-100777", Name: "team", Members: config.MembersOperators}},
	}, {ID: 2, Name: "side", Provider: "tg2", Owner: "alice", Target: config.TargetPrivate}}
	serving := false
	list := governorChannels(provs, chans, func() bool { return serving })
	assert.Empty(t, list(), "serving no request, it lists nothing")

	serving = true
	got := list()
	assert.Equal(t, []mcpsrv.ChannelInfo{
		{Name: "local", Kind: "local"},
		{Name: "chat", Kind: "bot", Groups: []mcpsrv.GroupInfo{{Name: "team", Members: "operators"}}},
		{Name: "side", Kind: "bot"},
	}, got)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	for _, secret := range []string{"CHAT_SECRET_VAR", "SIDE_SECRET_VAR", "hidden.example", "s3cr3t-value", "endpoint", "token", "-100777"} {
		assert.NotContains(t, string(b), secret, "what the governor reads")
	}
}

func TestTheGovernorServesWhileItsRequesterIsAnOperator(t *testing.T) {
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
		got := servesAnOperator(func() (string, string, bool) { return c.subject, c.channel, c.ok }, func(s string) bool { return flagged[s] })()
		assert.Equal(t, c.want, got, name)
	}
}
