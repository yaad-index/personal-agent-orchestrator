package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
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
		"a.yaml": "name: a\nbackend: claude-code\n" + startup + "levels:\n  reasoning: 1\n",
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
		"unknown key":     {map[string]string{"a.yaml": "name: a\nbackend: claude-code\nnope: 1\n" + startup}, "nope"},
		"missing name":    {map[string]string{"a.yaml": "backend: claude-code\n" + startup}, "name"},
		"bad name":        {map[string]string{"a.yaml": "name: Bad Name\nbackend: claude-code\n" + startup}, "name"},
		"missing backend": {map[string]string{"a.yaml": "name: a\n" + startup}, "backend is required"},
		"bad tag":         {map[string]string{"a.yaml": "name: a\nbackend: claude-code\ntags: [\"X Y\"]\n" + startup}, "tag"},
		"duplicate name": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\n" + startup,
			"b.yaml": "name: a\nbackend: claude-code\n" + startup,
		}, "already defined"},
		"no startup command": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  ready_pattern: r\n  ready_timeout: 1s\n",
		}, "startup.command"},
		"no ready pattern": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: c\n  ready_timeout: 1s\n",
		}, "startup.ready_pattern is required"},
		"invalid ready pattern": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: c\n  ready_pattern: \"(\"\n  ready_timeout: 1s\n",
		}, "startup.ready_pattern"},
		"no ready timeout": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: c\n  ready_pattern: r\n" + transcript,
		}, "startup.ready_timeout"},
		"no transcript format": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: c\n  ready_pattern: r\n  ready_timeout: 1s\ntranscript:\n  path: /t\n",
		}, "transcript.format"},
		"no transcript path": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: c\n  ready_pattern: r\n  ready_timeout: 1s\ntranscript:\n  format: f\n",
		}, "transcript.path"},
		"unknown level kind":     {map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup + "levels:\n  gossip: 1\n"}, "unknown kind"},
		"level below one":        {map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup + "levels:\n  message: 0\n"}, "at least 1"},
		"inherit not a category": {map[string]string{"a.yaml": "name: a\nbackend: claude-code\ninherit: [plugins]\n" + startup}, `"plugins" is not a category`},
		"an option the adapter owns": {map[string]string{
			"a.yaml": "name: a\nbackend: claude-code\nstartup:\n  command: claude --strict-mcp-config\n  ready_pattern: r\n  ready_timeout: 1s\n" + transcript,
		}, "sets --strict-mcp-config, which the \"claude-code\" backend adapter sets itself"},
		"a backend that cannot isolate": {map[string]string{"a.yaml": "name: a\nbackend: other\ninherit: [mcp]\n" + startup}, "cannot shut out instructions, skills, settings, memory"},
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
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup})

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
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup})
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
	write(t, path, "name: a\nbackend: claude-code\n"+startup)
	require.NoError(t, DecodeFile(path, &p))
	assert.NoError(t, ValidateProfile(p))
	p.Backend = ""
	assert.Error(t, ValidateProfile(p))

	write(t, path, "name: a\nunknown_key: 1\n")
	assert.Error(t, DecodeFile(path, &p), "unknown keys are an error")
}

func TestTempThresholdsDefaultAndValidate(t *testing.T) {
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup})
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
	for _, name := range append(slices.Clone(core.ReservedNames), core.CommandWords...) {
		_, err := Load(setup(t, map[string]string{"a.yaml": "name: " + name + "\nbackend: claude-code\n" + startup}))
		require.Error(t, err, name)
		assert.ErrorContains(t, err, "reserved")
	}
}

const meshFp1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const meshFp2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func meshSetup(t *testing.T, node, orch, doc string) string {
	t.Helper()
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup})
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

func channelsConfig(t *testing.T, channels string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+channels)
	write(t, filepath.Join(dir, "profiles", "a.yaml"), "name: a\nbackend: claude-code\n"+startup)
	_, err := Load(filepath.Join(dir, "pao.yaml"))
	return filepath.Join(dir, "pao.yaml"), err
}

func TestChannelsAreLoadedWithTheirIdNameKindCredentialAndOptions(t *testing.T) {
	path, err := channelsConfig(t, "channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_BOT\n    options:\n      poll: 30\n  - id: 2\n    name: alt-chat\n    kind: bot\n    credential_env: PAO_BOT2\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Orchestrator.Channels, 2)
	assert.Equal(t, Channel{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_BOT", Options: map[string]any{"poll": 30}}, cfg.Orchestrator.Channels[0])
	assert.Equal(t, 2, cfg.Orchestrator.Channels[1].ID)
}

func TestNoChannelsIsFine(t *testing.T) {
	path, err := channelsConfig(t, "")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Orchestrator.Channels)
}

func TestChannelsRejects(t *testing.T) {
	ok := "    kind: bot\n    credential_env: PAO_BOT\n"
	for name, c := range map[string]struct{ body, want string }{
		"no id":          {"channels:\n  - name: chat\n" + ok, "id must be a positive integer"},
		"zero id":        {"channels:\n  - id: 0\n    name: chat\n" + ok, "id must be a positive integer"},
		"negative id":    {"channels:\n  - id: -3\n    name: chat\n" + ok, "id must be a positive integer"},
		"no name":        {"channels:\n  - id: 1\n" + ok, "name"},
		"bad name":       {"channels:\n  - id: 1\n    name: Chat Room\n" + ok, "name"},
		"long name":      {"channels:\n  - id: 1\n    name: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" + ok, "name"},
		"reserved name":  {"channels:\n  - id: 1\n    name: local\n" + ok, "reserved"},
		"no kind":        {"channels:\n  - id: 1\n    name: chat\n    credential_env: PAO_BOT\n", "kind is required"},
		"no credential":  {"channels:\n  - id: 1\n    name: chat\n    kind: bot\n", "credential_env"},
		"bad credential": {"channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: \"has space\"\n", "credential_env"},
		"duplicate id":   {"channels:\n  - id: 1\n    name: a\n" + ok + "  - id: 1\n    name: b\n" + ok, "id 1 is already channel"},
		"duplicate name": {"channels:\n  - id: 1\n    name: a\n" + ok + "  - id: 2\n    name: a\n" + ok, "name \"a\" is already used"},
		"unknown field":  {"channels:\n  - id: 1\n    name: chat\n    token: x\n" + ok, "token"},
	} {
		_, err := channelsConfig(t, c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestUpstreamsAreLoadedWithTheirToolsTagsAndRisk(t *testing.T) {
	path, err := channelsConfig(t, "mcp_upstreams:\n  - name: search\n    url: https://mcp.example/mcp\n    credential_env: PAO_SEARCH\n    tools:\n      query:\n        tags: [research]\n        risk: read\n      purge: {}\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, []MCPUpstream{{Name: "search", URL: "https://mcp.example/mcp", CredentialEnv: "PAO_SEARCH", Tools: map[string]UpstreamTool{
		"query": {Tags: []string{"research"}, Risk: gate.RiskRead},
		"purge": {},
	}}}, cfg.Orchestrator.MCPUpstreams)
	assert.Equal(t, "search__query", ToolName("search", "query"))
}

func TestUpstreamsRejects(t *testing.T) {
	tools := "    tools:\n      query: {}\n"
	for name, c := range map[string]struct{ body, want string }{
		"no name":        {"mcp_upstreams:\n  - url: https://x/mcp\n" + tools, "name"},
		"underscore":     {"mcp_upstreams:\n  - name: a_b\n    url: https://x/mcp\n" + tools, "name"},
		"duplicate":      {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n" + tools + "  - name: a\n    url: https://y/mcp\n" + tools, `name "a" is already used`},
		"no url":         {"mcp_upstreams:\n  - name: a\n" + tools, "url"},
		"bad scheme":     {"mcp_upstreams:\n  - name: a\n    url: ftp://x/mcp\n" + tools, "url"},
		"bad credential": {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    credential_env: \"has space\"\n" + tools, "credential_env"},
		"no tools":       {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n", "at least one"},
		"bad tool":       {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    tools:\n      \"a b\": {}\n", `tool "a b"`},
		"long tool":      {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    tools:\n      " + strings.Repeat("t", 62) + ": {}\n", "at most 64"},
		"bad risk":       {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    tools:\n      q:\n        risk: harmless\n", "unknown risk"},
		"bad tag":        {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    tools:\n      q:\n        tags: [\"a b\"]\n", "tag"},
		"unknown field":  {"mcp_upstreams:\n  - name: a\n    url: https://x/mcp\n    token: x\n" + tools, "token"},
	} {
		_, err := channelsConfig(t, c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestChannelGroupsAreLoaded(t *testing.T) {
	path, err := channelsConfig(t, "channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_BOT\n    groups:\n      - id: \"-100200\"\n        name: group_channel_one\n        members: operators\n      - id: \"-300\"\n        name: ops-2\n        members: operators\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, []Group{{ID: "-100200", Name: "group_channel_one", Members: "operators"}, {ID: "-300", Name: "ops-2", Members: "operators"}}, cfg.Orchestrator.Channels[0].Groups)
}

func TestChannelGroupsRejects(t *testing.T) {
	head := "channels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_BOT\n    groups:\n"
	for name, c := range map[string]struct{ body, want string }{
		"no id":          {"      - name: a\n        members: operators\n", "groups[0]: id is required"},
		"blank id":       {"      - id: \" \"\n        name: a\n        members: operators\n", "id is required"},
		"padded id":      {"      - id: \" -5\"\n        name: a\n        members: operators\n", "no surrounding space"},
		"no name":        {"      - id: \"-5\"\n        members: operators\n", `name "" must be`},
		"bad name":       {"      - id: \"-5\"\n        name: Group One\n        members: operators\n", `name "Group One" must be`},
		"long name":      {"      - id: \"-5\"\n        name: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n        members: operators\n", "at most 32"},
		"no members":     {"      - id: \"-5\"\n        name: a\n", `members must be "operators"`},
		"other members":  {"      - id: \"-5\"\n        name: a\n        members: everyone\n", `members must be "operators"`},
		"duplicate id":   {"      - id: \"-5\"\n        name: a\n        members: operators\n      - id: \"-5\"\n        name: b\n        members: operators\n", `groups[1]: id "-5" is already listed`},
		"duplicate name": {"      - id: \"-5\"\n        name: a\n        members: operators\n      - id: \"-6\"\n        name: a\n        members: operators\n", `groups[1]: name "a" is already`},
		"unknown field":  {"      - id: \"-5\"\n        name: a\n        members: operators\n        output: group\n", "output"},
	} {
		_, err := channelsConfig(t, head+c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestAGroupNameIsUniqueAcrossTheNodesChannels(t *testing.T) {
	_, err := channelsConfig(t, "channels:\n  - id: 1\n    name: a\n    kind: bot\n    credential_env: PAO_BOT\n    groups:\n      - id: \"-5\"\n        name: team\n        members: operators\n"+
		"  - id: 2\n    name: b\n    kind: bot\n    credential_env: PAO_BOT2\n    groups:\n      - id: \"-6\"\n        name: team\n        members: operators\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `channels[1].groups[0]: name "team" is already`)
	assert.Contains(t, err.Error(), "channels[0].groups[0]", "the error says where the name was first used")
}

func TestTheSameGroupMayBeListedOnTwoChannels(t *testing.T) {
	g := func(name string) string {
		return "    groups:\n      - id: \"-5\"\n        name: " + name + "\n        members: operators\n"
	}
	_, err := channelsConfig(t, "channels:\n  - id: 1\n    name: a\n    kind: bot\n    credential_env: PAO_BOT\n"+g("via_a")+"  - id: 2\n    name: b\n    kind: bot\n    credential_env: PAO_BOT2\n"+g("via_b"))
	require.NoError(t, err)
}

const bindingsHead = "level: 2\nchannels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_BOT\n    groups:\n      - id: \"-5\"\n        name: team\n        members: operators\nbindings:\n"

func TestBindingsAreLoadedWithTheirDefaults(t *testing.T) {
	path, err := channelsConfig(t, bindingsHead+
		"  - agent: a\n    to: group:team\n"+
		"  - agent: a\n    stream: stdout\n    to: private:alice@chat\n    level: 3\n    prefix: \"TA:\"\n"+
		"  - agent: a\n    to: channel:local\n    level: 1\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, []Binding{
		{Agent: "a", Stream: "stdout", To: "group:team", Level: 2},
		{Agent: "a", Stream: "stdout", To: "private:alice@chat", Level: 3, Prefix: "TA:"},
		{Agent: "a", Stream: "stdout", To: "channel:local", Level: 1},
	}, cfg.Orchestrator.Bindings, "the stream defaults to stdout and the level to the node's")
}

func TestBindingsRejects(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"unknown agent":      {"  - agent: z\n    to: group:team\n", `agent "z" is not a configured agent`},
		"unknown stream":     {"  - agent: a\n    stream: stdin\n    to: group:team\n", `stream must be "stdout" or "stderr"`},
		"stderr level":       {"  - agent: a\n    stream: stderr\n    to: group:team\n    level: 2\n", "a stderr binding takes no level and no prefix"},
		"stderr prefix":      {"  - agent: a\n    stream: stderr\n    to: group:team\n    prefix: \"Q:\"\n", "a stderr binding takes no level and no prefix"},
		"stderr duplicate":   {"  - agent: a\n    stream: stderr\n    to: group:team\n  - agent: a\n    stream: stderr\n    to: group:team\n", `bindings[1]: agent "a" is already bound to group:team`},
		"no destination":     {"  - agent: a\n", `destination "" must be`},
		"unknown kind":       {"  - agent: a\n    to: room:team\n", `kind "room"`},
		"unknown group":      {"  - agent: a\n    to: group:other\n", `no admitted group is named "other"`},
		"bad subject":        {"  - agent: a\n    to: private:Al ice\n", "is not a subject name"},
		"unknown channel":    {"  - agent: a\n    to: private:alice@nope\n", `no channel is named "nope"`},
		"bad channel":        {"  - agent: a\n    to: private:alice@\n", "is not a channel name"},
		"channel with convs": {"  - agent: a\n    to: channel:chat\n", `channel "chat" has several conversations`},
		"negative level":     {"  - agent: a\n    to: group:team\n    level: -1\n", "level must be at least 1"},
		"long prefix":        {"  - agent: a\n    to: group:team\n    prefix: \"12345678901\"\n", "longer than 10"},
		"two-line prefix":    {"  - agent: a\n    to: group:team\n    prefix: \"a\\nb\"\n", "one line"},
		"duplicate":          {"  - agent: a\n    to: private:alice\n  - agent: a\n    to: private:alice\n    level: 3\n", `bindings[1]: agent "a" is already bound to private:alice at bindings[0]`},
		"unknown field":      {"  - agent: a\n    to: group:team\n    mode: quiet\n", "mode"},
		"unknown level word": {"  - agent: a\n    to: group:team\n    level: loud\n", `level must be a number or "log"`},
	} {
		_, err := channelsConfig(t, bindingsHead+c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestAStderrBindingIsLoadedBesideAStdoutOneToTheSameDestination(t *testing.T) {
	path, err := channelsConfig(t, bindingsHead+
		"  - agent: a\n    to: private:alice\n"+
		"  - agent: a\n    stream: stderr\n    to: private:alice\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, []Binding{
		{Agent: "a", Stream: "stdout", To: "private:alice", Level: 2},
		{Agent: "a", Stream: "stderr", To: "private:alice"},
	}, cfg.Orchestrator.Bindings, "a stderr binding gets no level")
}

func TestAPrefixOfTenCharactersIsAllowedCountedAsCharacters(t *testing.T) {
	_, err := channelsConfig(t, bindingsHead+"  - agent: a\n    to: group:team\n    prefix: \"ääääääääää\"\n")
	assert.NoError(t, err)
}

func TestParseDestinationRoundTrips(t *testing.T) {
	for _, s := range []string{"group:team", "private:alice", "private:alice@chat", "channel:local"} {
		d, err := ParseDestination(s)
		require.NoError(t, err, s)
		assert.Equal(t, s, d.String())
	}
}

func TestABindingAtTheLogLevel(t *testing.T) {
	path, err := channelsConfig(t, bindingsHead+"  - agent: a\n    to: group:team\n    level: log\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, BindingLevel(core.LevelLog), cfg.Orchestrator.Bindings[0].Level)
}

func TestTheGovernorIsLoaded(t *testing.T) {
	path, err := channelsConfig(t, "governor:\n  agent: a\n  propose: [agents, spawn]\n")
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, Governor{Agent: "a", Propose: []string{"agents", "spawn"}}, cfg.Orchestrator.Governor)

	path, err = channelsConfig(t, "governor:\n  agent: a\n  prefix: /ask\n")
	require.NoError(t, err)
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/ask", cfg.Orchestrator.Governor.Prefix)

	path, err = channelsConfig(t, "")
	require.NoError(t, err)
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, Governor{}, cfg.Orchestrator.Governor, "no governor by default")
}

func TestTheGovernorRejects(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"unknown agent":    {"governor:\n  agent: z\n", `governor.agent "z" is not a configured agent`},
		"a gate answer":    {"governor:\n  agent: a\n  propose: [agents, allow]\n", `governor.propose[1]: "allow" is not a command a governor may propose`},
		"help":             {"governor:\n  agent: a\n  propose: [help]\n", `governor.propose[0]: "help" is not`},
		"no such command":  {"governor:\n  agent: a\n  propose: [launch]\n", `"launch" is not`},
		"propose no agent": {"governor:\n  propose: [agents]\n", "governor.propose or governor.prefix is set but governor.agent is not"},
		"prefix no agent":  {"governor:\n  prefix: /ask\n", "governor.propose or governor.prefix is set but governor.agent is not"},
		"pao's prefix":     {"governor:\n  agent: a\n  prefix: /pao\n", `governor.prefix "/pao" must be`},
		"no slash":         {"governor:\n  agent: a\n  prefix: ask\n", `governor.prefix "ask" must be`},
		"two words":        {"governor:\n  agent: a\n  prefix: \"/ask me\"\n", `governor.prefix "/ask me" must be`},
		"unknown field":    {"governor:\n  agent: a\n  model: small\n", "model"},
		"bound": {"level: 2\nchannels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_BOT\n    groups:\n      - id: \"-5\"\n        name: team\n        members: operators\n" +
			"governor:\n  agent: a\nbindings:\n  - agent: a\n    to: group:team\n", `agent "a" is the governor: what it says goes only where it was asked`},
	} {
		_, err := channelsConfig(t, c.body)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func accountsConfig(t *testing.T, accounts, profile string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pao.yaml"), "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+accounts)
	write(t, filepath.Join(dir, "profiles", "a.yaml"), "name: a\nbackend: claude-code\n"+profile+startup)
	return Load(filepath.Join(dir, "pao.yaml"))
}

func TestAccountsAreLoadedAndAProfileNamesOne(t *testing.T) {
	cfg, err := accountsConfig(t, "accounts:\n  - name: sub\n    metered: true\n    reader: claude-subscription\n    credentials: creds.json\n  - name: local\n    metered: false\n", "account: sub\n")
	require.NoError(t, err)
	yes, no := true, false
	creds := cfg.Orchestrator.Accounts[0].Credentials
	assert.True(t, filepath.IsAbs(creds), "a relative credentials path is resolved against the config's directory")
	assert.Equal(t, "creds.json", filepath.Base(creds))
	assert.Equal(t, []Account{{Name: "sub", Metered: &yes, Reader: ReaderClaudeSubscription, Credentials: creds}, {Name: "local", Metered: &no}}, cfg.Orchestrator.Accounts)
	assert.Equal(t, Budget{Poll: DefaultBudgetPoll}, cfg.Orchestrator.Budget, "budget defaults")
	assert.Equal(t, "sub", cfg.Profiles[0].Account)
	a, ok := cfg.Orchestrator.Account("local")
	require.True(t, ok)
	assert.False(t, *a.Metered)
	_, ok = cfg.Orchestrator.Account("nope")
	assert.False(t, ok)
}

func TestAProfileWithoutAnAccountLoads(t *testing.T) {
	cfg, err := accountsConfig(t, "", "")
	require.NoError(t, err)
	assert.Empty(t, cfg.Profiles[0].Account)
	assert.NoError(t, cfg.Orchestrator.CheckAccount(""))
}

func TestAccountsRejects(t *testing.T) {
	for name, c := range map[string]struct{ accounts, profile, want string }{
		"metered left out":       {"accounts:\n  - name: a\n    reader: claude-subscription\n", "", "metered must be set"},
		"metered with no reader": {"accounts:\n  - name: a\n    metered: true\n", "", `reader "" is not one of claude-subscription`},
		"unknown reader":         {"accounts:\n  - name: a\n    metered: true\n    reader: guess\n    credentials: c.json\n", "", `reader "guess"`},
		"unmetered with reader":  {"accounts:\n  - name: a\n    metered: false\n    reader: claude-subscription\n", "", "this one is unmetered"},
		"bad name":               {"accounts:\n  - name: \"A b\"\n    metered: false\n", "", "name"},
		"duplicate name":         {"accounts:\n  - name: a\n    metered: false\n  - name: a\n    metered: false\n", "", `name "a" is already used`},
		"credential in config":   {"accounts:\n  - name: a\n    metered: false\n    token: x\n", "", "token"},
		"reader with no creds":   {"accounts:\n  - name: a\n    metered: true\n    reader: claude-subscription\n", "", "needs credentials"},
		"unmetered with creds":   {"accounts:\n  - name: a\n    metered: false\n    credentials: c.json\n", "", "this one is unmetered"},
		"negative poll":          {"budget:\n  poll: -1s\n", "", "budget.poll must be positive"},
		"node-wide floor":        {"budget:\n  floor: 10\n", "", "floor"},
		"unknown account":        {"accounts:\n  - name: a\n    metered: false\n", "account: b\n", `account "b" is not a configured account`},
		"account with none":      {"", "account: b\n", `account "b" is not a configured account`},
	} {
		_, err := accountsConfig(t, c.accounts, c.profile)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestAProfilesBudgetRuleIsLoaded(t *testing.T) {
	cfg, err := accountsConfig(t, "", "budget:\n  windows: [week]\n  floor: 20\n")
	require.NoError(t, err)
	assert.Equal(t, core.BudgetRule{Windows: []string{core.WindowWeek}, Floor: 20}, cfg.Profiles[0].Budget)
}

func TestAProfilesBudgetRuleRejects(t *testing.T) {
	for name, c := range map[string]struct{ profile, want string }{
		"unknown window":   {"budget:\n  windows: [month]\n", `window "month" is not one of week, five_hour`},
		"window twice":     {"budget:\n  windows: [week, week]\n", `window "week" is named twice`},
		"negative floor":   {"budget:\n  floor: -1\n", "floor -1 must be at least 0 and below 100"},
		"floor of 100":     {"budget:\n  floor: 100\n", "floor 100"},
		"unknown rule key": {"budget:\n  formula: x\n", "formula"},
	} {
		_, err := accountsConfig(t, "", c.profile)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}
