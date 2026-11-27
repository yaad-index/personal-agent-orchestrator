package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

func TestAnAgentsCallIntoPaoIsJudgedOnceAsPaosActionOnPaosSide(t *testing.T) {
	n := startNodeWith(t, map[string]string{"boss": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(goodRecipe("fake"), "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		// No stored answer: pao's sending has not been approved before.
		require.NoError(t, os.Remove(filepath.Join(cfg.Orchestrator.StateDir, "policy.json")))
	})
	ctx := context.Background()
	spawnTwo(t, n)
	_, err := n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:tmp-a", Kind: "agent", Name: "tmp-b"})
	require.NoError(t, err)

	// The agent's own hook lets its call to pao through: the agent is not the actor.
	d := decisionWithin(t, askGate(t, n, "mcp__pao__send_message", `{"name":"tmp-b","text":"hi"}`))
	assert.True(t, d.Allow, "%+v", d)

	a := mcpSession(t, n, agentToken(t, n, "tmp-a"))

	// Reading needs no question.
	assert.Equal(t, []string{"tmp-b"}, agentNames(mcpCall[listed](t, a, "list_agents", nil)))

	// Sending is pao's action, asked once, here.
	send := func(text string) <-chan *mcp.CallToolResult {
		out := make(chan *mcp.CallToolResult, 1)
		go func() {
			res, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"name": "tmp-b", "text": text}})
			if err != nil {
				res = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
			}
			out <- res
		}()
		return out
	}
	res := send("first")
	id := nextQuestion(t, n)
	require.NoError(t, n.client.Decide(ctx, id, "deny", "not now"))
	r := <-res
	assert.True(t, r.IsError)
	assert.Contains(t, r.Content[0].(*mcp.TextContent).Text, "denied: not now")

	res = send("second")
	id = nextQuestion(t, n)
	require.NoError(t, n.client.Decide(ctx, id, "allow", ""))
	r = <-res
	assert.False(t, r.IsError, "%+v", r.Content)
	got := n.next(t)
	assert.Equal(t, "echo: [tmp-a]: second", got.Text, "the denied message never arrived; the allowed one did")

	// A message to an agent out of reach is a not found, and asks nobody.
	nf, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"name": "boss", "text": "x"}})
	require.NoError(t, err)
	assert.True(t, nf.IsError)
	assert.NotContains(t, nf.Content[0].(*mcp.TextContent).Text, "denied")
	select {
	case m := <-n.events:
		assert.NotContains(t, m.Text, "/allow ", "no question was raised")
	case <-time.After(300 * time.Millisecond):
	}
}
