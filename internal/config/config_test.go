package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
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
		"b.yaml": "name: b\nbackend: claude-code\ntags: [x]\nworkdir: /tmp\n" + startup,
		"a.yaml": "name: a\nbackend: claude-code\n" + startup,
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "n1", cfg.Orchestrator.Node)
	require.Len(t, cfg.Profiles, 2)
	assert.Equal(t, "a", cfg.Profiles[0].Name, "profiles load in file-name order")
	assert.Equal(t, []string{"x"}, cfg.Profiles[1].Tags)
	assert.Equal(t, "/tmp", cfg.Profiles[1].Workdir)
	assert.Equal(t, 30*time.Second, cfg.Profiles[1].Startup.ReadyTimeout)
}

const startup = "startup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\n" + transcript

const transcript = "transcript:\n  format: claude-code\n  path: /tmp/t\n"

// TestLoadRejects gives each case exactly one defect and checks the error names
// it, so a case cannot pass by failing for some other reason.
func TestLoadLevelsOverrideDefaults(t *testing.T) {
	path := setup(t, map[string]string{
		"a.yaml": "name: a\nbackend: b\n" + startup + "levels:\n  reasoning: 1\n",
	})

	cfg, err := Load(path)
	require.NoError(t, err)
	got := cfg.Profiles[0].EffectiveLevels()
	assert.Equal(t, 1, got[core.KindReasoning], "the profile's level wins")
	assert.Equal(t, 1, got[core.KindMessage], "kinds the profile leaves out keep the default")
	assert.Equal(t, 4, got[core.KindOther])
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct {
		profiles map[string]string
		want     string
	}{
		"unknown key":     {map[string]string{"a.yaml": "name: a\nbackend: b\nnope: 1\n" + startup}, "nope"},
		"missing name":    {map[string]string{"a.yaml": "backend: b\n" + startup}, "name"},
		"bad name":        {map[string]string{"a.yaml": "name: Bad Name\nbackend: b\n" + startup}, "name"},
		"missing backend": {map[string]string{"a.yaml": "name: a\n" + startup}, "backend is required"},
		"bad tag":         {map[string]string{"a.yaml": "name: a\nbackend: b\ntags: [\"X Y\"]\n" + startup}, "tag"},
		"duplicate name": {map[string]string{
			"a.yaml": "name: a\nbackend: b\n" + startup,
			"b.yaml": "name: a\nbackend: b\n" + startup,
		}, "already defined"},
		"no startup command": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  ready_pattern: r\n  ready_timeout: 1s\n",
		}, "startup.command"},
		"no ready pattern": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  command: c\n  ready_timeout: 1s\n",
		}, "startup.ready_pattern is required"},
		"invalid ready pattern": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  command: c\n  ready_pattern: \"(\"\n  ready_timeout: 1s\n",
		}, "startup.ready_pattern"},
		"no ready timeout": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  command: c\n  ready_pattern: r\n" + transcript,
		}, "startup.ready_timeout"},
		"no transcript format": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  command: c\n  ready_pattern: r\n  ready_timeout: 1s\ntranscript:\n  path: /t\n",
		}, "transcript.format"},
		"no transcript path": {map[string]string{
			"a.yaml": "name: a\nbackend: b\nstartup:\n  command: c\n  ready_pattern: r\n  ready_timeout: 1s\ntranscript:\n  format: f\n",
		}, "transcript.path"},
		"unknown level kind": {map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup + "levels:\n  gossip: 1\n"}, "unknown kind"},
		"level below one":    {map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup + "levels:\n  message: 0\n"}, "at least 1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(setup(t, tc.profiles))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
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
