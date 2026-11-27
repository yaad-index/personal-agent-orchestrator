package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// deniedIn is the permissions.deny the agent was started with, read from the
// arguments the fake backend wrote beside its transcript.
func deniedIn(t *testing.T, argsFile string) []string {
	t.Helper()
	var b []byte
	require.Eventually(t, func() bool {
		var err error
		b, err = os.ReadFile(argsFile)
		return err == nil && len(b) > 0
	}, 10*time.Second, 10*time.Millisecond, "the agent did not start")
	args := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			var s struct {
				Permissions struct {
					Deny []string `json:"deny"`
				} `json:"permissions"`
			}
			require.NoError(t, json.Unmarshal([]byte(args[i+1]), &s))
			return s.Permissions.Deny
		}
	}
	t.Fatalf("no --settings in %q", args)
	return nil
}

func TestTheNodesDefaultToolsReachEveryAgentThatDoesNotGiveItsOwn(t *testing.T) {
	interactive := []string{"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"}
	none := []string{}
	n := startNodeWith(t, map[string]string{"a": "", "b": ""}, func(dir, script string, cfg *config.Config) {
		cfg.Orchestrator.ProfileDefaults.Tools = core.Tools{Disable: &interactive}
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == "b" {
				cfg.Profiles[i].Tools = core.Tools{Disable: &none}
			}
		}
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(tempRecipe(script, "fake", "READY>", "claude-code")), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
	})
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		require.NoError(t, n.client.AgentAction(ctx, name, "start"))
	}
	assert.Equal(t, interactive, deniedIn(t, filepath.Join(n.dir, "transcripts", "a.jsonl.args")), "the default")
	assert.Empty(t, deniedIn(t, filepath.Join(n.dir, "transcripts", "b.jsonl.args")), "its own empty list replaces the default")

	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "", "")
	require.NoError(t, err)
	assert.Equal(t, interactive, deniedIn(t, filepath.Join(n.stateDir, "temp", "tmp-a", "t.jsonl.args")), "a temporary agent takes the default too")
}
