package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credConfig writes a node with the given body, and a secret file at
// secret.txt beside pao.yaml, then loads it.
func credConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "secret.txt"), "value\n")
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+body)
	write(t, filepath.Join(dir, "profiles", "a.yaml"), "name: a\nbackend: claude-code\n"+startup)
	return Load(filepath.Join(dir, "pao.yaml"))
}

func TestAChannelNamesItsCredentialAsASecret(t *testing.T) {
	cfg, err := credConfig(t, "secrets:\n  - name: bot-token\n    file: secret.txt\n  - name: other\n    env: OTHER\n"+
		"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: bot-token\n")
	require.NoError(t, err)
	assert.Equal(t, "bot-token", cfg.Orchestrator.Channels[0].CredentialSecret)
	assert.True(t, cfg.Orchestrator.BoundToChannel("bot-token"))
	assert.False(t, cfg.Orchestrator.BoundToChannel("other"))
	assert.False(t, cfg.Orchestrator.BoundToChannel(""))
}

func TestAChannelNamesItsCredentialOneWay(t *testing.T) {
	for name, ch := range map[string]string{
		"neither": "    kind: bot\n",
		"both":    "    kind: bot\n    credential_env: E\n    credential_secret: s\n",
	} {
		_, err := credConfig(t, "secrets:\n  - name: s\n    env: S_VAR\nchannels:\n  - id: 1\n    name: chat\n"+ch)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "credential_env or credential_secret, one of them", name)
	}
}

func TestAChannelsSecretIsItsAlone(t *testing.T) {
	up := func(cred string) string {
		return "mcp_upstreams:\n  - name: srv\n    url: https://mcp.example/mcp\n" + cred + "    tools:\n      q: {}\n"
	}
	for name, c := range map[string]struct{ body, want string }{
		"no such secret": {"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: nope\n", `no secret named "nope"`},
		"injected": {"secrets:\n  - name: s\n    env: S_VAR\n    inject:\n      hosts: [api.example]\n      env: S_INJ\n" +
			"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n", "injected through the egress proxy"},
		"a server's": {"secrets:\n  - name: s\n    env: S_VAR\n" + up("    secret: s\n") +
			"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n", "already a server's credential"},
		"another channel's": {"secrets:\n  - name: s\n    env: S_VAR\n" +
			"channels:\n  - id: 1\n    name: a\n    kind: bot\n    credential_secret: s\n  - id: 2\n    name: b\n    kind: bot\n    credential_secret: s\n", `already the credential of channel "a"`},
	} {
		_, err := credConfig(t, c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

// Point 3 is on the place a value is read from, not only on names: every way
// another reader could share it is refused.
func TestAChannelsCredentialPlaceHoldsNothingElse(t *testing.T) {
	up := "mcp_upstreams:\n  - name: srv\n    url: https://mcp.example/mcp\n    credential_env: E\n    tools:\n      q: {}\n"
	for name, c := range map[string]struct{ body, want string }{
		"direct variable, a secret in it": {"secrets:\n  - name: t\n    env: E\n" +
			"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: E\n", `environment variable E, which also holds secret "t"`},
		"secret's variable, another secret in it": {"secrets:\n  - name: s\n    env: E\n  - name: t\n    env: E\n" +
			"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n", `which also holds secret "t"`},
		"secret's file, another secret in it": {"secrets:\n  - name: s\n    file: secret.txt\n  - name: t\n    file: ./secret.txt\n" +
			"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n", `which also holds secret "t"`},
		"two channels, one variable": {"channels:\n  - id: 1\n    name: a\n    kind: bot\n    credential_env: E\n  - id: 2\n    name: b\n    kind: bot\n    credential_env: E\n",
			`which also holds channel "b"'s credential`},
		"a channel's secret, another's variable": {"secrets:\n  - name: s\n    env: E\n" +
			"channels:\n  - id: 1\n    name: a\n    kind: bot\n    credential_secret: s\n  - id: 2\n    name: b\n    kind: bot\n    credential_env: E\n", `also holds channel "b"'s credential`},
		"a server's variable": {up + "channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: E\n", `also holds server "srv"'s credential`},
	} {
		_, err := credConfig(t, c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
		assert.Contains(t, err.Error(), "ADR-0057 point 3", name)
	}
}

func TestASecondNameForTheSameFileIsTheSamePlace(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "secret.txt"), "value\n")
	require.NoError(t, os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "alias.txt")))
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+
		"secrets:\n  - name: s\n    file: secret.txt\n  - name: t\n    file: alias.txt\n"+
		"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n")
	_, err := Load(filepath.Join(dir, "pao.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `also holds secret "t"`)
}

func TestAChannelsOwnReferenceIsNoClash(t *testing.T) {
	_, err := credConfig(t, "secrets:\n  - name: s\n    env: E\n  - name: unrelated\n    env: F\n"+
		"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_secret: s\n  - id: 2\n    name: alt\n    kind: bot\n    credential_env: G\n")
	require.NoError(t, err)
}

func TestAManagedChannelsCredentialErrorNamesTheManagedFile(t *testing.T) {
	pao, mf := managedSetup(t, "secrets:\n  - name: t\n    env: E\n", "channels:\n  - id: 1\n    name: ui-made\n    kind: bot\n    credential_env: E\n")
	_, err := Load(pao)
	require.Error(t, err)
	assert.Contains(t, err.Error(), mf+`: channel "ui-made"`)
}
