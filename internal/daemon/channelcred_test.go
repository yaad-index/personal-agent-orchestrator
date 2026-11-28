package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

func TestAProvidersCredentialIsReadThroughWhatItNames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bot.token")
	require.NoError(t, os.WriteFile(file, []byte("from-file\n"), 0o600))
	t.Setenv("PAO_TEST_DIRECT", "direct-value")
	o := config.Orchestrator{
		Secrets: []config.Secret{{Name: "in-var", Env: "PAO_TEST_SECRET_VAR"}, {Name: "in-file", File: file}, {Name: "gone", File: filepath.Join(dir, "missing")}},
		Providers: []config.Provider{
			{Name: "direct", CredentialEnv: "PAO_TEST_DIRECT"},
			{Name: "via-var", CredentialSecret: "in-var"},
			{Name: "via-file", CredentialSecret: "in-file"},
			{Name: "unreadable", CredentialSecret: "gone"},
		},
	}
	creds := providerCredentials(o, map[string]string{"in-var": "from-var"})
	assert.Equal(t, "direct-value", creds["direct"])
	assert.Equal(t, "from-var", creds["via-var"], "a secret's variable as read at start")
	assert.Equal(t, "from-file", creds["via-file"], "a file's value, without its line end")
	assert.Empty(t, creds["unreadable"], "no credential, for the adapter to report")
	_, set := os.LookupEnv("PAO_TEST_DIRECT")
	assert.False(t, set, "the variable is taken out of the environment agents inherit")
}

func TestAProvidersSecretIsNeverATasks(t *testing.T) {
	o := config.Orchestrator{
		Secrets:   []config.Secret{{Name: "bot", Env: "B"}, {Name: "free", Env: "F"}},
		Providers: []config.Provider{{Name: "tg", CredentialSecret: "bot"}},
	}
	has := taskHasSecret(o)
	assert.False(t, has("bot"), "to a task, a provider's secret is not configured")
	assert.True(t, has("free"))
	_, err := resolveSecret(o, map[string]string{"bot": "v", "free": "w"})("bot")
	require.Error(t, err, "and it is never resolved into a run")
	assert.Contains(t, err.Error(), `no secret named "bot"`)
}
