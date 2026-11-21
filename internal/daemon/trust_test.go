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

// trustingAgent writes a fake backend that first asks whether to trust its folder,
// as Claude Code does on a new working directory, and is ready only once
// answered.
func trustingAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust.sh")
	require.NoError(t, os.WriteFile(path, []byte(`printf 'Quick safety check: Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n'
read -r answer
echo 'READY>'
exec cat
`), 0o700))
	return path
}

func TestAConfiguredAgentsTrustQuestionIsAnsweredAndATemporaryAgentsIsNot(t *testing.T) {
	script := trustingAgent(t)
	n := startNodeWith(t, map[string]string{"bee": "sh " + script}, func(dir, _ string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "cloned.yaml"), []byte(tempRecipe(script, "cloned", "READY>", "claude-code")), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
	})
	ctx := context.Background()

	require.NoError(t, n.client.AgentAction(ctx, "bee", "start"), "the operator's own agent: its question is answered, and it becomes ready")

	_, err := n.client.Spawn(ctx, "cloned", "tmp-a", "", "")
	require.Error(t, err, "a temporary agent's question is left for a person, so it does not become ready")
	assert.Contains(t, err.Error(), "not ready")
}
