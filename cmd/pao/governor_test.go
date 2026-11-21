package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/governor"
)

func governorConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	path := filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(path, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+extra), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "gov.yaml"), []byte("name: gov\nbackend: claude-code\nstartup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\ntranscript:\n  format: claude-code\n  path: /tmp/t\n"), 0o600))
	return path
}

func TestGovernorInstructionsAreTheNodes(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, GovernorInstructionsCmd{Path: governorConfig(t, "governor:\n  agent: gov\n")}.run(&out))
	assert.Equal(t, governor.Instructions("/gpao", nil), out.String(), "the default prefix, every proposable command")

	out.Reset()
	require.NoError(t, GovernorInstructionsCmd{Path: governorConfig(t, "governor:\n  agent: gov\n  prefix: /ask\n  propose: [agents]\n")}.run(&out))
	assert.Equal(t, governor.Instructions("/ask", []string{"agents"}), out.String(), "the node's prefix and narrower set")

	err := GovernorInstructionsCmd{Path: governorConfig(t, "")}.run(&out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no governor is configured")
}
