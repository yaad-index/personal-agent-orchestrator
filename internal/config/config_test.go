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
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n")
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

func TestOrchestratorDefaultsAndResolution(t *testing.T) {
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup})

	cfg, err := Load(path)
	require.NoError(t, err)
	o := cfg.Orchestrator
	assert.Equal(t, DefaultListen, o.Listen)
	assert.Equal(t, DefaultTmuxSocket, o.TmuxSocket)
	assert.Equal(t, DefaultMessageWindow, o.MessageWindow)
	assert.Equal(t, DefaultLevel, o.Level)
	assert.Equal(t, filepath.Join(filepath.Dir(path), "state"), o.StateDir, "a relative state_dir is resolved against the config")
	assert.Equal(t, "PAO_TOKEN", o.TokenEnv)
	assert.Equal(t, []string{"read"}, o.Gate.AutoAllow)
	assert.Equal(t, DefaultAskTimeout, o.Gate.AskTimeout)
}

func TestGateSettings(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pao.yaml"), "node: n\nprofiles_dir: p\nstate_dir: s\ntoken_env: T\ngate:\n  auto_allow: [read, private]\n  ask_timeout: 90s\n")
	cfg, err := Load(filepath.Join(dir, "pao.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"read", "private"}, cfg.Orchestrator.Gate.AutoAllow)
	assert.Equal(t, 90*time.Second, cfg.Orchestrator.Gate.AskTimeout)

	for name, body := range map[string]string{
		"unknown risk":     "gate:\n  auto_allow: [spicy]\n",
		"negative timeout": "gate:\n  ask_timeout: -1s\n",
	} {
		write(t, filepath.Join(dir, "bad.yaml"), "node: n\nprofiles_dir: p\nstate_dir: s\ntoken_env: T\n"+body)
		_, err := Load(filepath.Join(dir, "bad.yaml"))
		assert.Error(t, err, name)
	}
}

func TestOrchestratorRejects(t *testing.T) {
	base := "node: n\nprofiles_dir: p\nstate_dir: s\ntoken_env: T\n"
	cases := map[string]struct{ body, want string }{
		"no state dir":     {"node: n\nprofiles_dir: p\ntoken_env: T\n", "state_dir is required"},
		"no token env":     {"node: n\nprofiles_dir: p\nstate_dir: s\n", "token_env is required"},
		"public listen":    {base + "listen: 0.0.0.0:7744\n", "not a loopback address"},
		"hostname listen":  {base + "listen: example.com:7744\n", "not a loopback address"},
		"listen no port":   {base + "listen: 127.0.0.1\n", "listen"},
		"window below one": {base + "message_window: -1\n", "message_window"},
		"level below one":  {base + "level: -2\n", "level"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "pao.yaml"), tc.body)
			_, err := Load(filepath.Join(dir, "pao.yaml"))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
		})
	}

	for _, ok := range []string{"127.0.0.1:1", "[::1]:1", "localhost:1"} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "pao.yaml"), base+"listen: \""+ok+"\"\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "p"), 0o755))
		_, err := Load(filepath.Join(dir, "pao.yaml"))
		assert.NoError(t, err, ok)
	}
}

func TestLoadOrchestratorErrors(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, "nonode.yaml"), "profiles_dir: p\nstate_dir: s\ntoken_env: T\n")
	_, err := Load(filepath.Join(dir, "nonode.yaml"))
	assert.ErrorContains(t, err, "node is required")

	write(t, filepath.Join(dir, "nodir.yaml"), "node: n\nstate_dir: s\ntoken_env: T\n")
	_, err = Load(filepath.Join(dir, "nodir.yaml"))
	assert.ErrorContains(t, err, "profiles_dir is required")

	_, err = Load(filepath.Join(dir, "absent.yaml"))
	assert.Error(t, err)
}

func TestRecipesDirDefaultsAndResolution(t *testing.T) {
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup})
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(path), "state", "recipes"), cfg.Orchestrator.RecipesDir, "by default under the state dir")

	dir := filepath.Dir(path)
	write(t, path, "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nrecipes_dir: my-recipes\n")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "my-recipes"), cfg.Orchestrator.RecipesDir, "a relative recipes_dir is resolved against the config")

	write(t, path, "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nrecipes_dir: /srv/recipes\n")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/srv/recipes", cfg.Orchestrator.RecipesDir)
}

func TestValidateProfileAndDecodeFileAreTheSameRulesAsLoad(t *testing.T) {
	var p core.Profile
	path := filepath.Join(t.TempDir(), "p.yaml")
	write(t, path, "name: a\nbackend: b\n"+startup)
	require.NoError(t, DecodeFile(path, &p))
	assert.NoError(t, ValidateProfile(p))
	p.Backend = ""
	assert.Error(t, ValidateProfile(p))

	write(t, path, "name: a\nunknown_key: 1\n")
	assert.Error(t, DecodeFile(path, &p), "unknown keys are an error")
}

func TestTempThresholdsDefaultAndValidate(t *testing.T) {
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup})
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, DefaultUnusedAfter, cfg.Orchestrator.Temp.UnusedAfter)
	assert.Equal(t, DefaultIdleAfter, cfg.Orchestrator.Temp.IdleAfter)

	base := "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"
	write(t, path, base+"temp:\n  unused_after: 2m\n  idle_after: 1h\n")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Minute, cfg.Orchestrator.Temp.UnusedAfter)
	assert.Equal(t, time.Hour, cfg.Orchestrator.Temp.IdleAfter)

	write(t, path, base+"temp:\n  idle_after: -1m\n")
	_, err = Load(path)
	assert.Error(t, err)
}

func TestAProfileMayNotBeNamedLikePao(t *testing.T) {
	for _, name := range core.ReservedNames {
		_, err := Load(setup(t, map[string]string{"a.yaml": "name: " + name + "\nbackend: b\n" + startup}))
		require.Error(t, err, name)
		assert.ErrorContains(t, err, "reserved")
	}
}

const meshFp1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const meshFp2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func meshSetup(t *testing.T, node, orch, doc string) string {
	t.Helper()
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: b\n" + startup})
	dir := filepath.Dir(path)
	write(t, path, "node: "+node+"\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+orch)
	if doc != "" {
		write(t, filepath.Join(dir, "mesh.yaml"), doc)
	}
	return path
}

const meshDoc = "master: n1\nnodes:\n  - id: n1\n    addr: 10.0.0.1:7745\n    fingerprint: " + meshFp1 + "\n  - id: n2\n    addr: 10.0.0.2:7745\n    fingerprint: " + meshFp2 + "\n"

func TestAMeshIsLoadedAlongsideTheConfig(t *testing.T) {
	path := meshSetup(t, "n1", "mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n", meshDoc)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Mesh)
	assert.Len(t, cfg.Mesh.Nodes, 2)
	assert.Equal(t, filepath.Join(filepath.Dir(path), "mesh.yaml"), cfg.Orchestrator.MeshFile, "a relative path is resolved against the config")
	assert.Equal(t, "0.0.0.0:7745", cfg.Orchestrator.MeshListen, "the mesh listener is for other machines: not limited to loopback")
	assert.Equal(t, filepath.Join(filepath.Dir(path), "state", "mesh.key"), cfg.Orchestrator.MeshKey, "by default the key lives in the state dir")
}

func TestAConfigWithoutAMeshFileIsASingleNode(t *testing.T) {
	cfg, err := Load(meshSetup(t, "n1", "", ""))
	require.NoError(t, err)
	assert.Nil(t, cfg.Mesh)
}

func TestMeshSettingsMustBeConsistent(t *testing.T) {
	cases := map[string]struct{ orch, doc, want string }{
		"file without listen":  {"mesh_file: mesh.yaml\n", meshDoc, "mesh_listen is required"},
		"listen without file":  {"mesh_listen: 0.0.0.0:7745\n", "", "mesh_file is not"},
		"bad listen":           {"mesh_file: mesh.yaml\nmesh_listen: nowhere\n", meshDoc, "host:port"},
		"this node not listed": {"mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n", "master: n2\nnodes:\n  - id: n2\n    addr: h:1\n    fingerprint: " + meshFp2 + "\n", `this node ("n1") is not in the mesh document`},
		"missing document":     {"mesh_file: gone.yaml\nmesh_listen: 0.0.0.0:7745\n", "", "gone.yaml"},
		"invalid document":     {"mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n", "master: n1\nnodes: []\n", "lists no nodes"},
	}
	for name, c := range cases {
		_, err := Load(meshSetup(t, "n1", c.orch, c.doc))
		require.Error(t, err, name)
		assert.ErrorContains(t, err, c.want, name)
	}
}

func TestLoadWithoutMeshDoesNotNeedTheNodeToBeListedYet(t *testing.T) {
	// The command that creates the node's key runs before the node can be in a
	// document that names its key.
	path := meshSetup(t, "n3", "mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n", meshDoc)
	_, err := Load(path)
	require.Error(t, err, "n3 is not in the document")
	cfg, err := LoadWithoutMesh(path)
	require.NoError(t, err)
	assert.Equal(t, "n3", cfg.Orchestrator.Node)
	assert.Nil(t, cfg.Mesh, "it did not read the document")
	assert.Equal(t, filepath.Join(filepath.Dir(path), "mesh.yaml"), cfg.Orchestrator.MeshFile)

	path = meshSetup(t, "n3", "mesh_file: nowhere.yaml\nmesh_listen: 0.0.0.0:7745\n", "")
	_, err = LoadWithoutMesh(path)
	assert.NoError(t, err, "a document that is not there yet is not an error for keygen")
}

func TestMeshKeyAndFileResolution(t *testing.T) {
	path := meshSetup(t, "n1", "mesh_file: /abs/mesh.yaml\nmesh_listen: 0.0.0.0:7745\nmesh_key: keys/n1.key\n", "")
	cfg, err := LoadWithoutMesh(path)
	require.NoError(t, err)
	assert.Equal(t, "/abs/mesh.yaml", cfg.Orchestrator.MeshFile)
	assert.Equal(t, filepath.Join(filepath.Dir(path), "keys", "n1.key"), cfg.Orchestrator.MeshKey)
}
