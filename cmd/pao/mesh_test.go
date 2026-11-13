package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
)

func meshNode(t *testing.T, node, extra string) (cfgPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o700))
	cfgPath = filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("node: "+node+"\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+extra), 0o600))
	return cfgPath, dir
}

func TestKeygenCreatesTheKeyAndSaysWhatToListWithoutTheNodeBeingListedYet(t *testing.T) {
	cfgPath, dir := meshNode(t, "n1", "mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n")

	var out bytes.Buffer
	require.NoError(t, MeshKeygenCmd{Config: cfgPath}.run(&out))

	m := regexp.MustCompile(`fingerprint: (sha256:[0-9a-f]{64})`).FindStringSubmatch(out.String())
	require.Len(t, m, 2, out.String())
	id, err := mesh.Load(filepath.Join(dir, "state", "mesh.key"), "n1")
	require.NoError(t, err)
	assert.Equal(t, id.Fingerprint, m[1], "the fingerprint printed is the key's")
	assert.Contains(t, out.String(), "- id: n1")
	info, err := os.Stat(filepath.Join(dir, "state", "mesh.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestKeygenRefusesToReplaceTheKey(t *testing.T) {
	cfgPath, dir := meshNode(t, "n1", "")
	var out bytes.Buffer
	require.NoError(t, MeshKeygenCmd{Config: cfgPath}.run(&out))
	before, err := os.ReadFile(filepath.Join(dir, "state", "mesh.key"))
	require.NoError(t, err)

	err = MeshKeygenCmd{Config: cfgPath}.run(&out)
	require.Error(t, err)
	assert.ErrorContains(t, err, "identity in every other node's list")
	after, err := os.ReadFile(filepath.Join(dir, "state", "mesh.key"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestShowReportsTheNodeAndWhetherItsKeyMatchesTheMeshDocument(t *testing.T) {
	cfgPath, dir := meshNode(t, "n1", "mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n")
	var out bytes.Buffer
	require.NoError(t, MeshKeygenCmd{Config: cfgPath}.run(&out))
	id, err := mesh.Load(filepath.Join(dir, "state", "mesh.key"), "n1")
	require.NoError(t, err)
	other := "sha256:" + string(bytes.Repeat([]byte("2"), 64))

	doc := func(fp string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mesh.yaml"), []byte("master: n1\nnodes:\n  - id: n1\n    addr: 10.0.0.1:7745\n    fingerprint: "+fp+"\n  - id: n2\n    addr: 10.0.0.2:7745\n    fingerprint: "+other+"\n"), 0o600))
	}
	doc(id.Fingerprint)
	out.Reset()
	require.NoError(t, MeshShowCmd{Config: cfgPath}.run(&out))
	assert.Contains(t, out.String(), "matches the mesh document")
	assert.Regexp(t, `\* n1\s+10\.0\.0\.1:7745`, out.String())
	assert.Regexp(t, `  n2\s+10\.0\.0\.2:7745`, out.String())
	assert.Contains(t, out.String(), "listens:     0.0.0.0:7745")

	doc("sha256:" + string(bytes.Repeat([]byte("3"), 64)))
	out.Reset()
	require.NoError(t, MeshShowCmd{Config: cfgPath}.run(&out))
	assert.Contains(t, out.String(), "DOES NOT MATCH", "a key that is not the one the others trust is said so, loudly")
}

func TestShowOnASingleNode(t *testing.T) {
	cfgPath, _ := meshNode(t, "n1", "")
	var out bytes.Buffer
	require.NoError(t, MeshShowCmd{Config: cfgPath}.run(&out))
	assert.Contains(t, out.String(), "not part of a mesh")
}

func TestShowWithoutAKeyIsAnError(t *testing.T) {
	cfgPath, dir := meshNode(t, "n1", "mesh_file: mesh.yaml\nmesh_listen: 0.0.0.0:7745\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mesh.yaml"), []byte("master: n1\nnodes:\n  - id: n1\n    addr: h:1\n    fingerprint: sha256:"+string(bytes.Repeat([]byte("1"), 64))+"\n"), 0o600))
	var out bytes.Buffer
	assert.Error(t, MeshShowCmd{Config: cfgPath}.run(&out))
}
