package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

func TestTheRecipesAreListedForTheOperator(t *testing.T) {
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(tempRecipe(script, "fake", "READY>", "claude-code")), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
	})
	rs, err := n.client.Recipes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "fake", rs[0].Name)
	assert.Equal(t, "a fake agent", rs[0].Description)
	assert.Equal(t, []string{"temp"}, rs[0].Tags)
}

func TestTheChannelsListedForTheUIHoldNothingButNames(t *testing.T) {
	chans := []config.Channel{{
		ID: 1, Name: "tg", Kind: "telegram", CredentialEnv: "TG_SECRET_VAR",
		Options: map[string]any{"api": "https://adapter-option.example"},
		Groups: []config.Group{{ID: "-100777/1", Name: "family", Members: config.MembersOperators,
			Topics: []config.Topic{{ID: "42", Name: "family-news"}}}},
	}}
	got := channelsOf(chans)
	assert.Equal(t, []local.ChannelJSON{
		{Name: "local", Kind: "local", Groups: []string{}},
		{Name: "tg", Kind: "telegram", Groups: []string{"family", "family-news"}},
	}, got)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	for _, never := range []string{"TG_SECRET_VAR", "adapter-option", "-100777", `"42"`} {
		assert.NotContains(t, string(b), never)
	}
}
