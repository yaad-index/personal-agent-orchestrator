package daemon

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

func TestTheNodesNarrowerSetBoundsWhatItsGovernorProposes(t *testing.T) {
	n := startNodeWith(t, map[string]string{"gov": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Governor = config.Governor{Agent: "gov", Propose: []string{"agents"}}
	}, withAgentsAllowed)
	ctx := context.Background()

	_, err := n.client.Post(ctx, "alice", "/gpao list my agents", "name", "gov")
	require.NoError(t, err)
	assert.Equal(t, "echo: [alice]: list my agents", n.next(t).Text, "the request reached the governor, which answers alice")

	cs := mcpSession(t, n, agentToken(t, n, "gov"))
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "propose_command", Arguments: map[string]any{"command": "/pao view gov pause"}})
	require.NoError(t, err)
	require.True(t, res.IsError, "view is outside the node's set")
	assert.Contains(t, toolText(res), `"view" is not a command a governor may propose`)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "propose_command", Arguments: map[string]any{"command": "/pao agents"}})
	require.NoError(t, err)
	require.False(t, res.IsError, toolText(res))
	assert.Contains(t, n.next(t).Text, "gov proposes:\n/pao agents")
}

func toolText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if c, ok := res.Content[0].(*mcp.TextContent); ok {
		return c.Text
	}
	return ""
}

func TestTheGovernorStartsOnItsFirstRequestAndAfreshForAnotherSubject(t *testing.T) {
	n := startNodeWith(t, map[string]string{"gov": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Governor = config.Governor{Agent: "gov"}
	})
	ctx := context.Background()
	require.False(t, tmuxHasSession(t, n, "gov"), "not started with the node")

	_, err := n.client.Post(ctx, "alice", "/gpao list my agents", "name", "gov")
	require.NoError(t, err)
	assert.Equal(t, "echo: [alice]: list my agents", n.next(t).Text)
	require.True(t, tmuxHasSession(t, n, "gov"), "started by the first request")
	first := panePID(t, n, "gov")

	_, err = n.client.Post(ctx, "alice", "/gpao and again", "name", "gov")
	require.NoError(t, err)
	assert.Equal(t, "echo: [alice]: and again", n.next(t).Text)
	assert.Equal(t, first, panePID(t, n, "gov"), "the same subject keeps its session")

	_, err = n.client.Post(ctx, "bob", "/gpao pause it", "name", "gov")
	require.NoError(t, err)
	assert.Equal(t, "echo: [bob]: pause it", n.next(t).Text)
	assert.NotEqual(t, first, panePID(t, n, "gov"), "another subject is answered in a new session")
}

func TestTheGovernorReadsWhatItsOwnGrantsReach(t *testing.T) {
	n := startNodeWith(t, map[string]string{"gov": "", "bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Governor = config.Governor{Agent: "gov"}
	})
	ctx := context.Background()
	// The operator reaches gov and bee; the governor's own grants reach all three.
	for _, g := range []local.GrantJSON{{Subject: "operator", Kind: "agent", Name: "gov"}, {Subject: "operator", Kind: "agent", Name: "bee"}, {Subject: "agent:gov", Kind: "agent", Tag: "team"}} {
		_, err := n.client.AddGrant(ctx, g)
		require.NoError(t, err)
	}
	_, err := n.client.RemoveGrant(ctx, local.GrantJSON{Subject: "operator", Kind: "agent", Tag: "team"})
	require.NoError(t, err)
	names := func(cs *mcp.ClientSession) []string {
		out := mcpCall[struct {
			Agents []struct {
				Name string `json:"name"`
			} `json:"agents"`
		}](t, cs, "list_agents", nil)
		var got []string
		for _, a := range out.Agents {
			got = append(got, a.Name)
		}
		return got
	}

	channels := func(cs *mcp.ClientSession) []string {
		out := mcpCall[struct {
			Channels []struct {
				Name string `json:"name"`
			} `json:"channels"`
		}](t, cs, "list_channels", nil)
		var got []string
		for _, c := range out.Channels {
			got = append(got, c.Name)
		}
		return got
	}

	require.NoError(t, n.client.AgentAction(ctx, "gov", "start"))
	cs := mcpSession(t, n, agentToken(t, n, "gov"))
	assert.Empty(t, names(cs), "serving no request, the governor reads nothing")
	assert.Empty(t, channels(cs), "not even the channels")

	_, err = n.client.Post(ctx, "alice", "/gpao list my agents", "name", "gov")
	require.NoError(t, err)
	assert.Equal(t, "echo: [alice]: list my agents", n.next(t).Text)
	cs = mcpSession(t, n, agentToken(t, n, "gov"))
	assert.ElementsMatch(t, []string{"gov", "bee", "wasp"}, names(cs), "what its own grants reach: wasp too, though the requester does not reach it")
	assert.Equal(t, []string{"local"}, channels(cs))

	tools, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	var offered []string
	for _, tl := range tools.Tools {
		offered = append(offered, tl.Name)
	}
	assert.ElementsMatch(t, []string{"list_agents", "list_recipes", "list_channels"}, offered, "only the tools matching needs: no send, spawn, kill or harvest")
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"name": "bee", "text": "stop"}})
	assert.Error(t, err, "the governor sends to no agent, though its grants reach bee")
}
