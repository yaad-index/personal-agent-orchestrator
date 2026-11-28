package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/defaultagent"
)

// ownedSetup is two channels of alice's and one of bob's, and agents of
// alice's, of bob's and of no one's.
func ownedSetup(t *testing.T) (defaultsAPI, *defaultagent.Store) {
	t.Helper()
	store, err := defaultagent.Open(filepath.Join(t.TempDir(), "d.json"))
	require.NoError(t, err)
	users := map[string]string{"ana": "alice", "ari": "alice", "bo": "bob", "nobody": ""}
	resolve := func(tg core.Target) ([]core.Profile, error) {
		u, ok := users[tg.Value]
		if !ok {
			return nil, core.ErrNoMatch
		}
		return []core.Profile{{Name: tg.Value, User: u}}, nil
	}
	chs := []config.Channel{
		{Name: "a1", Owner: "alice", Target: config.TargetPrivate},
		{Name: "a2", Owner: "alice", Target: "-5", Groups: []config.Group{{ID: "-5", Name: "crew", Members: config.MembersOperators}}},
		{Name: "b1", Owner: "bob", Target: config.TargetPrivate},
	}
	return newDefaultsAPI(store, chs, resolve), store
}

func TestAChannelsAgentBelongsToItsOwner(t *testing.T) {
	a, _ := ownedSetup(t)
	ctx := context.Background()
	_, err := a.SetDefault(ctx, "a1", "bo")
	assert.ErrorIs(t, err, local.ErrBadRequest)
	assert.ErrorContains(t, err, `channel "a1" is alice's and agent "bo" is bob's`)
	_, err = a.SetDefault(ctx, "a1", "nobody")
	assert.ErrorIs(t, err, local.ErrBadRequest)
	assert.ErrorContains(t, err, `agent "nobody" belongs to no user`)
	_, err = a.SetDefault(ctx, "group:crew", "bo")
	assert.ErrorIs(t, err, local.ErrBadRequest, "a group's default is its channel's agent too")

	_, err = a.SetDefault(ctx, "a1", "ana")
	require.NoError(t, err)
	_, err = a.SetDefault(ctx, "b1", "bo")
	require.NoError(t, err)
}

func TestAnAgentMovedToAnotherChannelLeavesItsFirst(t *testing.T) {
	a, _ := ownedSetup(t)
	ctx := context.Background()
	_, err := a.SetDefault(ctx, "a1", "ana")
	require.NoError(t, err)
	ch, err := a.SetDefault(ctx, "a2", "ana")
	require.NoError(t, err)
	assert.Equal(t, "a1", ch.Left, "an agent has one channel: it leaves the first")
	_, ok := a.Get("a1")
	assert.False(t, ok)
}

func TestAStoredDefaultOfAnotherOwnersAgentHasNoEffect(t *testing.T) {
	a, store := ownedSetup(t)
	_, err := store.Set("a1", "bo")
	require.NoError(t, err)
	_, ok := a.Get("a1")
	assert.False(t, ok, "kept in the store, it routes nothing")
	_, err = store.Set("b1", "bo")
	require.NoError(t, err)
	agent, ok := a.Get("b1")
	assert.True(t, ok, "on its owner's channel it does")
	assert.Equal(t, "bo", agent)
}
