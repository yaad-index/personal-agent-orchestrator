package daemon

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	cs := mcpSession(t, n, sessionEnv(t, n, "gov", "PAO_AGENT_TOKEN"))
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
