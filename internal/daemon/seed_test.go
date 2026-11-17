package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/grants"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
)

func recipesIn(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600))
	}
	return dir
}

func TestTheSeedIsATagScopePerTagAndACellPerUntaggedAgent(t *testing.T) {
	dir := recipesIn(t, map[string]string{
		"tagged": tempRecipe("/bin/true", "tagged", "READY>", "claude-code"),
	})
	set, err := recipe.LoadDir(dir)
	require.NoError(t, err)
	profiles := []core.Profile{
		{Name: "a", Tags: []string{"home", "team"}},
		{Name: "b", Tags: []string{"team"}},
		{Name: "solo"},
	}
	seed, untagged := operatorSeed(profiles, set)
	op := access.Operator
	assert.Equal(t, []access.Grant{
		{Subject: op, Kind: access.ResourceAgent, Name: "solo"},
		{Subject: op, Kind: access.ResourceAgent, Tag: "home"},
		{Subject: op, Kind: access.ResourceAgent, Tag: "team"},
		{Subject: op, Kind: access.ResourceAgent, Tag: "temp"},
	}, seed, "tagged agents are reached by their tags, the recipe's tag too, and only the untagged agent by name")
	assert.Empty(t, untagged)
}

func TestARecipeWithNoTagIsReportedAndNotGuessed(t *testing.T) {
	body := strings.Replace(tempRecipe("/bin/true", "bare", "READY>", "claude-code"), "  tags: [temp]\n", "", 1)
	require.NotContains(t, body, "tags:")
	set, err := recipe.LoadDir(recipesIn(t, map[string]string{"bare": body}))
	require.NoError(t, err)
	require.Equal(t, []string{"bare"}, set.Names())
	seed, untagged := operatorSeed(nil, set)
	assert.Empty(t, seed, "no grant can name an agent whose name is chosen at spawn")
	assert.Equal(t, []string{"bare"}, untagged)
}

func TestTheSeedIsWrittenOnlyForANodeThatRanBeforeAndHasNoGrants(t *testing.T) {
	profiles := []core.Profile{{Name: "assistant", Tags: []string{"home"}}}
	open := func(p string) *grants.Store {
		s, err := grants.Open(p)
		require.NoError(t, err)
		return s
	}

	fresh := filepath.Join(t.TempDir(), "grants.json")
	require.NoError(t, seedOperator(open(fresh), false, profiles, ""))
	assert.NoFileExists(t, fresh, "a node that never ran starts with no grants")

	upgraded := filepath.Join(t.TempDir(), "grants.json")
	require.NoError(t, seedOperator(open(upgraded), true, profiles, ""))
	assert.Equal(t, grants.Document{Version: 1, Grants: []grants.Entry{{Subject: "operator", Kind: "agent", Tag: "home"}}}, open(upgraded).Document())

	s := open(upgraded)
	_, err := s.Remove(access.Grant{Subject: access.Operator, Kind: access.ResourceAgent, Tag: "home"})
	require.NoError(t, err)
	require.NoError(t, seedOperator(open(upgraded), true, profiles, ""))
	assert.Empty(t, open(upgraded).Document().Grants, "a revoked seed is not written back at the next start")
}
