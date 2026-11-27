package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
