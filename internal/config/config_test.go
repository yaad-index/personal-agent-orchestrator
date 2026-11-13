package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func setup(t *testing.T, profiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\n")
	for name, body := range profiles {
		write(t, filepath.Join(dir, "profiles", name), body)
	}
	return filepath.Join(dir, "pao.yaml")
}

func TestLoad(t *testing.T) {
	path := setup(t, map[string]string{
		"b.yaml": "name: b\nbackend: claude-code\ntags: [x]\nworkdir: /tmp\n",
		"a.yaml": "name: a\nbackend: claude-code\n",
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "n1", cfg.Orchestrator.Node)
	require.Len(t, cfg.Profiles, 2)
	assert.Equal(t, "a", cfg.Profiles[0].Name, "profiles load in file-name order")
	assert.Equal(t, []string{"x"}, cfg.Profiles[1].Tags)
	assert.Equal(t, "/tmp", cfg.Profiles[1].Workdir)
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown key":     {"a.yaml": "name: a\nbackend: b\nnope: 1\n"},
		"missing name":    {"a.yaml": "backend: b\n"},
		"bad name":        {"a.yaml": "name: Bad Name\nbackend: b\n"},
		"missing backend": {"a.yaml": "name: a\n"},
		"bad tag":         {"a.yaml": "name: a\nbackend: b\ntags: [\"X Y\"]\n"},
		"duplicate name":  {"a.yaml": "name: a\nbackend: b\n", "b.yaml": "name: a\nbackend: b\n"},
	}
	for name, profiles := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(setup(t, profiles))
			assert.Error(t, err)
		})
	}
}

func TestLoadOrchestratorErrors(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, "nonode.yaml"), "profiles_dir: p\n")
	_, err := Load(filepath.Join(dir, "nonode.yaml"))
	assert.ErrorContains(t, err, "node is required")

	write(t, filepath.Join(dir, "nodir.yaml"), "node: n\n")
	_, err = Load(filepath.Join(dir, "nodir.yaml"))
	assert.ErrorContains(t, err, "profiles_dir is required")

	_, err = Load(filepath.Join(dir, "absent.yaml"))
	assert.Error(t, err)
}
