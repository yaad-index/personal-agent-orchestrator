package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

type listed struct {
	Agents []struct {
		Name string `json:"name"`
	} `json:"agents"`
}

func agentNames(l listed) []string {
	out := []string{}
	for _, a := range l.Agents {
		out = append(out, a.Name)
	}
	return out
}

func TestAGrantMadeAtRuntimeBoundsWhatAnAgentReachesAndARevocationTakesItBack(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	spawnTwo(t, n)
	ctx := context.Background()
	a := mcpSession(t, n, sessionEnv(t, n, "tmp-a", "PAO_AGENT_TOKEN"))

	assert.Empty(t, agentNames(mcpCall[listed](t, a, "list_agents", nil)), "no grant, nothing reached")

	ch, err := n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:tmp-a", Kind: "agent", Name: "tmp-b"})
	require.NoError(t, err)
	assert.True(t, ch.Changed)
	assert.Equal(t, []string{"tmp-b"}, agentNames(mcpCall[listed](t, a, "list_agents", nil)), "a cell reaches one agent")
	_ = mcpCall[struct {
		Delivered []struct{ Agent string } `json:"delivered"`
	}](t, a, "send_message", map[string]any{"name": "tmp-b", "text": "psst"})
	assert.Equal(t, "echo: [tmp-a]: psst", n.next(t).Text)
	res, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"name": "boss", "text": "hi"}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "boss is not granted")

	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:tmp-a", Kind: "agent", Tag: "team"})
	require.NoError(t, err)
	assert.Contains(t, agentNames(mcpCall[listed](t, a, "list_agents", nil)), "boss", "a tag scope reaches every agent carrying the tag")

	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:tmp-b", Kind: "agent", Name: "boss"})
	require.NoError(t, err)
	gs, err := n.client.Grants(ctx, "agent:tmp-a")
	require.NoError(t, err)
	assert.Equal(t, local.GrantsJSON{Version: 3, Grants: []local.GrantJSON{
		{Subject: "agent:tmp-a", Kind: "agent", Tag: "team"},
		{Subject: "agent:tmp-a", Kind: "agent", Name: "tmp-b"},
	}}, gs)

	for _, g := range gs.Grants {
		ch, err := n.client.RemoveGrant(ctx, g)
		require.NoError(t, err)
		assert.True(t, ch.Changed)
	}
	assert.Empty(t, agentNames(mcpCall[listed](t, a, "list_agents", nil)), "revoked at once")
	ch, err = n.client.RemoveGrant(ctx, local.GrantJSON{Subject: "agent:tmp-a", Kind: "agent", Name: "tmp-b"})
	require.NoError(t, err)
	assert.False(t, ch.Changed)

	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "tmp-a", Kind: "agent", Name: "tmp-b"})
	assert.ErrorContains(t, err, "400")
	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "agent:tmp-a", Kind: "tool", Name: "x"})
	assert.ErrorContains(t, err, "400", "only agents are enforced so far")
}

func TestGrantsAreReloadedAtStart(t *testing.T) {
	n := startNodeWith(t, map[string]string{"boss": ""}, func(_, _ string, cfg *config.Config) {
		require.NoError(t, os.MkdirAll(cfg.Orchestrator.StateDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, grantsFile),
			[]byte(`{"version":4,"grants":[{"subject":"agent:boss","kind":"agent","tag":"team"}]}`), 0o600))
	})
	gs, err := n.client.Grants(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, local.GrantsJSON{Version: 4, Grants: []local.GrantJSON{{Subject: "agent:boss", Kind: "agent", Tag: "team"}}}, gs)
}

func TestABadGrantsFileStopsTheNode(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	require.NoError(t, os.MkdirAll(state, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, grantsFile), []byte(`{"version":1,"grants":[{"subject":"bee","kind":"agent","name":"b"}]}`), 0o600))
	cfg := &config.Config{Orchestrator: config.Orchestrator{Node: "n1", StateDir: state, TmuxSocket: "pao-unused", MessageWindow: 10, Level: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Run(ctx, cfg, token, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), grantsFile)
}
