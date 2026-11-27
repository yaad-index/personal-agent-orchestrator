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
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

func TestAServersSecretIsNeverATasks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(file, []byte("server-key\n"), 0o600))
	o := config.Orchestrator{
		Secrets:      []config.Secret{{Name: "srv", File: file}, {Name: "task", Env: "TASK_SECRET"}},
		MCPUpstreams: []config.MCPUpstream{{Name: "s", URL: "https://mcp.example", Secret: "srv"}},
	}
	has := taskHasSecret(o)
	assert.True(t, has("task"))
	assert.False(t, has("srv"), "a task naming a server's secret is told there is no such secret")
	assert.False(t, has("missing"))

	resolve := resolveSecret(o, map[string]string{"task": "t-value"})
	_, err := resolve("srv")
	require.Error(t, err)
	assert.Equal(t, `no secret named "srv" is configured`, err.Error(), "the same words as a secret that does not exist")
	v, err := resolve("task")
	require.NoError(t, err)
	assert.Equal(t, "t-value", string(v))
}

func TestAServersCredentialIsReadAfreshFromItsSecret(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(file, []byte("first\n"), 0o600))
	o := config.Orchestrator{Secrets: []config.Secret{{Name: "srv", File: file}}}
	read := upstreamCredential(config.MCPUpstream{Secret: "srv"}, o, nil)
	v, err := read()
	require.NoError(t, err)
	assert.Equal(t, "first\n", v, "the client trims it")
	require.NoError(t, os.WriteFile(file, []byte("second"), 0o600))
	v, err = read()
	require.NoError(t, err)
	assert.Equal(t, "second", v, "a rotated file is read at the next connection")

	t.Setenv("OLD_STYLE", "env-token")
	read = upstreamCredential(config.MCPUpstream{CredentialEnv: "OLD_STYLE"}, o, nil)
	v, err = read()
	require.NoError(t, err)
	assert.Equal(t, "env-token", v, "credential_env keeps working")

	assert.Nil(t, upstreamCredential(config.MCPUpstream{}, o, nil), "a server with no credential has no reader")
}

func TestATaskOnTheNodeCannotNameAServersSecret(t *testing.T) {
	n := startNodeWith(t, map[string]string{"a": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "srv", Env: "PAO_TEST_SRV_SECRET"}, {Name: "mine", Env: "PAO_TEST_MINE_SECRET"}}
		cfg.Orchestrator.MCPUpstreams = []config.MCPUpstream{{Name: "s", URL: "http://127.0.0.1:1/mcp", Secret: "srv", Tools: map[string]config.UpstreamTool{"q": {}}}}
	})
	ctx := context.Background()
	_, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}, Attachments: task.Attachments{Secret: "srv"}})
	require.Error(t, err)
	assert.ErrorContains(t, err, "400")
	assert.ErrorContains(t, err, `no secret named "srv" is configured`, "to a task, a server's secret does not exist")
	_, err = n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}, Attachments: task.Attachments{Secret: "mine"}})
	require.NoError(t, err, "a secret that is no server's is a task's as before")
}
