package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.Name("pao"))
	require.NoError(t, err)

	ctx, err := parser.Parse([]string{"version"})
	require.NoError(t, err)
	assert.Equal(t, "version", ctx.Command())
}

func TestBuildVersionNotEmpty(t *testing.T) {
	assert.NotEmpty(t, buildVersion())
}

func TestConfigCheckRunsAgainstALoadableConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pao.yaml"), []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "a.yaml"), []byte("name: a\nbackend: claude-code\nstartup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\ntranscript:\n  format: claude-code\n  path: /tmp/t\n"), 0o600))

	assert.NoError(t, ConfigCheckCmd{Path: filepath.Join(dir, "pao.yaml")}.Run())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "b.yaml"), []byte("name: a\nbackend: x\nstartup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\ntranscript:\n  format: claude-code\n  path: /tmp/t\n"), 0o600))
	assert.Error(t, ConfigCheckCmd{Path: filepath.Join(dir, "pao.yaml")}.Run(), "a duplicate agent name fails the check")
}

// writeConfig writes a loadable orchestrator config naming PAO_RUN_TEST_TOKEN.
func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	path := filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(path, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_RUN_TEST_TOKEN\n"), 0o600))
	return path
}
