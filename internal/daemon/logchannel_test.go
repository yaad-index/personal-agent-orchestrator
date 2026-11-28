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
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

func logProfile(name, log string) core.Profile {
	return core.Profile{Name: name, User: "alice", LogChannel: log, Transcript: core.Transcript{Format: "claude-code"}}
}

var logChs = []config.Channel{
	{Name: "dm", Owner: "alice", Target: config.TargetPrivate},
	{Name: "logs", Owner: "alice", Target: "-5"},
	{Name: "more", Owner: "alice", Target: "-6"},
}

func TestTheLogChannelsAreTheProfilesAsTheyAreNow(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Add(logProfile("a", "logs")))
	require.NoError(t, reg.Add(logProfile("b", "")))
	require.NoError(t, reg.Add(logProfile("c", "gone")))
	logs := logChannelsOf(reg, logChs)
	assert.Equal(t, map[string]string{"logs": "a"}, logs(), "a channel the node does not run is no one's log channel")
	require.NoError(t, reg.Replace(logProfile("b", "more")))
	assert.Equal(t, map[string]string{"logs": "a", "more": "b"}, logs(), "read afresh")
}

func TestAProfileSavedWhileTheNodeRunsHasItsLogChannelChecked(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Add(logProfile("a", "logs")))
	check := savedProfileCheck(reg, logChs)
	require.NoError(t, check(logProfile("a", "logs")), "its own, saved again")
	require.NoError(t, check(logProfile("b", "more")))
	assert.ErrorContains(t, check(logProfile("b", "logs")), `already agent "a"'s`)
	assert.ErrorContains(t, check(logProfile("b", "dm")), "private conversation")
	p := logProfile("b", "")
	p.Transcript.Format = "nope"
	assert.ErrorContains(t, check(p), "unknown transcript format", "and what it checked before")
}

func TestALogChannelIsNoAgentsDefaultAndNoOnesPermissionChannel(t *testing.T) {
	dir := t.TempDir()
	store, err := defaultagent.Open(filepath.Join(dir, "defaults.json"))
	require.NoError(t, err)
	resolve := func(tg core.Target) ([]core.Profile, error) {
		return []core.Profile{{Name: tg.Value, User: "alice"}}, nil
	}
	logs := func() map[string]string { return map[string]string{"logs": "a"} }
	d := newDefaultsAPI(store, logChs, resolve)
	d.logs = logs
	ctx := context.Background()
	_, err = d.SetDefault(ctx, "logs", "b")
	require.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), `channel "logs" is agent "a"'s log channel`)
	_, err = d.SetDefault(ctx, "more", "b")
	require.NoError(t, err, "another of her group channels")

	ids, err := identity.Open(filepath.Join(dir, "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ids.Close() })
	require.NoError(t, ids.AddSubject("alice"))
	a := identityAPI{s: ids, owners: channelOwners(logChs), logs: logs}
	err = a.SetPermission(ctx, "alice", "logs")
	require.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), "no question there could be answered")
	require.NoError(t, a.SetPermission(ctx, "alice", "more"))
}
