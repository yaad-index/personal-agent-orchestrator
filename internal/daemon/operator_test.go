package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

var (
	opTeam = local.GrantJSON{Subject: "operator", Kind: "agent", Tag: "team"}
	opTemp = local.GrantJSON{Subject: "operator", Kind: "agent", Tag: "temp"}
)

func TestTheOperatorReachesOnlyWhatItIsGrantedAndCanAlwaysGrantItselfIn(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()

	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "", "")
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "hello", "name", "boss")
	require.NoError(t, err, "granted: routed")
	_ = n.next(t)

	for _, g := range []local.GrantJSON{opTeam, opTemp} {
		ch, err := n.client.RemoveGrant(ctx, g)
		require.NoError(t, err)
		require.True(t, ch.Changed)
	}

	_, err = n.client.Post(ctx, "human", "hello", "name", "boss")
	assert.ErrorContains(t, err, "404", "a message to an agent out of reach is answered as one to no agent")
	_, err = n.client.Post(ctx, "human", "hello", "tag", "team")
	assert.ErrorContains(t, err, "404", "and a tag reaches none of them")
	assert.ErrorContains(t, n.client.AgentAction(ctx, "boss", "restart"), `404: no agent "boss"`)
	assert.ErrorContains(t, n.client.SetStream(ctx, "boss", "local", 0), `404: no agent "boss"`)
	_, err = n.client.Spawn(ctx, "fake", "tmp-b", "", "")
	assert.ErrorContains(t, err, "404")
	_, err = n.client.Kill(ctx, "tmp-a")
	assert.ErrorContains(t, err, "404")
	agents, err := n.client.Agents(ctx)
	require.NoError(t, err)
	assert.Empty(t, agents, "the temporary agents out of reach are not listed")
	_, ok := agentByName(t, n, "tmp-a")
	assert.False(t, ok)
	assert.True(t, tmuxHasSession(t, n, "tmp-a"), "and nothing was done to them")

	// Administering the matrix needs no grant: the operator grants itself back in.
	_, err = n.client.AddGrant(ctx, opTeam)
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "back", "name", "boss")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: back", n.next(t).Text)
	require.NoError(t, n.client.AgentAction(ctx, "boss", "restart"))
	_, err = n.client.Kill(ctx, "tmp-a")
	assert.ErrorContains(t, err, "404", "team does not reach an agent tagged temp")
}

func withoutGrantsFile(t *testing.T, ranBefore bool) func(string, string, *config.Config) {
	return func(_, _ string, cfg *config.Config) {
		require.NoError(t, os.Remove(filepath.Join(cfg.Orchestrator.StateDir, grantsFile)))
		if ranBefore {
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, "messages.db"), nil, 0o600))
		}
	}
}

func TestANodeThatRanBeforeIsSeededSoItsOperatorKeepsItsAgents(t *testing.T) {
	n := startNodeWith(t, map[string]string{"boss": ""}, withoutGrantsFile(t, true))
	ctx := context.Background()
	gs, err := n.client.Grants(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, local.GrantsJSON{Version: 1, Grants: []local.GrantJSON{opTeam}}, gs, "explicit, listed grants")
	_, err = n.client.Post(ctx, "human", "still here", "name", "boss")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: still here", n.next(t).Text)
}

func TestANodeThatNeverRanStartsWithNoGrantsTheOperatorIncluded(t *testing.T) {
	n := startNodeWith(t, map[string]string{"boss": ""}, withoutGrantsFile(t, false))
	ctx := context.Background()
	gs, err := n.client.Grants(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, local.GrantsJSON{Grants: []local.GrantJSON{}}, gs)
	_, err = n.client.Post(ctx, "human", "hello", "name", "boss")
	assert.ErrorContains(t, err, "404")

	_, err = n.client.AddGrant(ctx, local.GrantJSON{Subject: "operator", Kind: "agent", Name: "boss"})
	require.NoError(t, err)
	_, err = n.client.Post(ctx, "human", "hello", "name", "boss")
	require.NoError(t, err, "once granted in")
	assert.Equal(t, "echo: [human]: hello", n.next(t).Text)
}
