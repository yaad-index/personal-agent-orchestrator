package mcpsrv

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// swappableGrants are grants a test changes while the server runs.
type swappableGrants struct {
	mu sync.Mutex
	l  grantList
}

func (g *swappableGrants) Of(s access.Subject) []access.Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.l.Of(s)
}

func (g *swappableGrants) set(l grantList) {
	g.mu.Lock()
	g.l = l
	g.mu.Unlock()
}

var governorMayPropose = access.Grant{Subject: access.Agent("governor"), Kind: access.ResourceTool, Name: ToolPropose}

func proposeServer(t *testing.T, ops Operations, grants access.Grants, asked *[]gate.Request) string {
	t.Helper()
	return serve(t, ops, staticResolver{"g": access.Agent("governor"), "b": access.Agent("boss"), "op": access.Operator},
		WithAuthorizer(access.Matrix{Grants: grants}), WithGate(recordingGate{allow: true, asked: asked}))
}

func TestOnlyASubjectGrantedTheToolIsOfferedIt(t *testing.T) {
	grants := grantList{governorMayPropose, {Subject: access.Operator, Kind: access.ResourceTool, Name: ToolPropose}}
	url := proposeServer(t, sampleOps, grants, &[]gate.Request{})
	assert.Contains(t, listed(t, connect(t, url, "g")), ToolPropose, "granted by name")
	assert.NotContains(t, listed(t, connect(t, url, "b")), ToolPropose, "not granted")
	assert.NotContains(t, listed(t, connect(t, url, "op")), ToolPropose, "the operator proposes to no one: it types")
}

func TestAProposalIsHandedToTheNodeAsTheCaller(t *testing.T) {
	var calls []opCall
	ops := sampleOps
	ops.calls = &calls
	asked := &[]gate.Request{}
	cs := connect(t, proposeServer(t, ops, grantList{governorMayPropose}, asked), "g")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolPropose, Arguments: map[string]any{"command": "  /pao view coder pause "}})
	require.NoError(t, err)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Equal(t, map[string]any{"shown": "/pao view coder pause"}, res.StructuredContent)
	assert.Equal(t, []opCall{{"propose", access.Agent("governor"), []string{"  /pao view coder pause "}}}, calls, "the node checks the text")
	require.Len(t, *asked, 1)
	assert.Equal(t, ActionPropose, (*asked)[0].Tool, "the gate judges pao's action")

	ops.opErr = errors.New(`"help" is not a command a governor may propose`)
	cs = connect(t, proposeServer(t, ops, grantList{governorMayPropose}, &[]gate.Request{}), "g")
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolPropose, Arguments: map[string]any{"command": "/pao help"}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a refusal reaches the governor")
}

func TestAGrantRevokedAfterTheToolWasOfferedIsRefusedAtTheCall(t *testing.T) {
	var calls []opCall
	ops := sampleOps
	ops.calls = &calls
	grants := &swappableGrants{l: grantList{governorMayPropose}}
	cs := connect(t, proposeServer(t, ops, grants, &[]gate.Request{}), "g")
	require.Contains(t, listed(t, cs), ToolPropose)

	grants.set(nil)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolPropose, Arguments: map[string]any{"command": "/pao agents"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown tool "propose_command"`)
	assert.Empty(t, calls, "nothing is proposed")
}

func TestTheGateAllowsAProposalAndTheHookCallOfTheTool(t *testing.T) {
	var allowed []string
	for _, r := range GateRules(DefaultServerName) {
		if r.Effect == gate.EffectAllow {
			allowed = append(allowed, r.Tool)
		}
	}
	assert.Contains(t, allowed, "mcp__pao__"+ToolPropose)
	assert.Contains(t, allowed, ActionPropose)
	assert.Contains(t, allowed, "mcp__pao__"+ToolListChannels)
	assert.Contains(t, GateRules(DefaultServerName), gate.Rule{Tool: ActionListChannels, Risk: gate.RiskRead})
}

func TestTheGovernorIsOfferedOnlyTheToolsMatchingNeeds(t *testing.T) {
	url := serve(t, sampleOps, staticResolver{"g": access.Agent("governor"), "b": access.Agent("boss"), "op": access.Operator},
		WithAuthorizer(access.Matrix{Grants: grantList{governorMayPropose}}), WithGate(recordingGate{allow: true, asked: &[]gate.Request{}}), WithGovernor("governor"),
		WithChannels(func() []ChannelInfo { return nil }))
	assert.ElementsMatch(t, []string{"list_agents", "list_recipes", ToolListChannels, ToolPropose}, slices.Collect(maps.Keys(listed(t, connect(t, url, "g")))))
	assert.ElementsMatch(t, ownTools, slices.Collect(maps.Keys(listed(t, connect(t, url, "b")))), "another agent is offered every one of pao's own tools, and ownTools names them all")
	assert.NotContains(t, listed(t, connect(t, url, "op")), ToolListChannels, "nor the operator")

	url = serve(t, sampleOps, staticResolver{"g": access.Agent("governor")},
		WithAuthorizer(access.Matrix{Grants: grantList{governorMayPropose}}), WithGovernor("governor"))
	assert.NotContains(t, listed(t, connect(t, url, "g")), ToolListChannels, "no channel lister, no tool")
}

func TestTheGovernorListsTheChannelsItIsGivenOnceTheGateAllows(t *testing.T) {
	chans := []ChannelInfo{{Name: "local", Kind: "local"}, {Name: "chat", Kind: "bot", Groups: []GroupInfo{{Name: "team", Members: "operators"}}}}
	asked := &[]gate.Request{}
	url := serve(t, sampleOps, staticResolver{"g": access.Agent("governor")},
		WithAuthorizer(access.Matrix{Grants: grantList{}}), WithGate(recordingGate{allow: true, asked: asked}), WithGovernor("governor"),
		WithChannels(func() []ChannelInfo { return chans }))
	got := call[listChannelsOutput](t, connect(t, url, "g"), ToolListChannels)
	assert.Equal(t, chans, got.Channels)
	require.Len(t, *asked, 1)
	assert.Equal(t, ActionListChannels, (*asked)[0].Tool, "the gate judges pao's action")

	url = serve(t, sampleOps, staticResolver{"g": access.Agent("governor")},
		WithAuthorizer(access.Matrix{Grants: grantList{}}), WithGate(recordingGate{allow: false, asked: &[]gate.Request{}}), WithGovernor("governor"),
		WithChannels(func() []ChannelInfo { return chans }))
	res, err := connect(t, url, "g").CallTool(context.Background(), &mcp.CallToolParams{Name: ToolListChannels})
	require.NoError(t, err)
	assert.True(t, res.IsError, "denied, nothing listed")
	assert.Nil(t, res.StructuredContent)
}
