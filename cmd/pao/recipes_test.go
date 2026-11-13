package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRecipe = `name: researcher
description: reads and reports
profile:
  backend: harness
  startup:
    command: "run-agent {name}"
    ready_pattern: "READY>"
    ready_timeout: 30s
  transcript:
    format: harness-jsonl
workdir:
  files:
    README.md: hello
`

func recipeConfig(t *testing.T) (cfgPath, recipesDir string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o700))
	return cfgPath, filepath.Join(dir, "state", "recipes")
}

func TestRecipesListShowsNameAndDescription(t *testing.T) {
	_, dir := recipeConfig(t)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "researcher.yaml"), []byte(testRecipe), 0o600))

	var out bytes.Buffer
	require.NoError(t, listRecipes(&out, dir))
	assert.Contains(t, out.String(), "researcher")
	assert.Contains(t, out.String(), "reads and reports")
}

func TestRecipesListOnNoDirIsEmptyNotAnError(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, listRecipes(&out, filepath.Join(t.TempDir(), "none")))
	assert.Empty(t, out.String())
}

func TestRecipesPullInstallsUnderTheConfiguredDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cfgPath, dir := recipeConfig(t)
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "researcher.yaml"), []byte(testRecipe), 0o600))
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "c"}} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}

	var out bytes.Buffer
	require.NoError(t, RecipesPullCmd{Config: cfgPath, URL: repo, Name: "team", AllowLocal: true}.run(&out))
	assert.Equal(t, "researcher\n", out.String())
	assert.FileExists(t, filepath.Join(dir, "team", "researcher.yaml"))

	err := RecipesPullCmd{Config: cfgPath, URL: repo, Name: "team", AllowLocal: true}.run(&out)
	require.Error(t, err)
	assert.ErrorContains(t, err, "--replace")

	require.Error(t, RecipesPullCmd{Config: cfgPath, URL: repo, Name: "other"}.run(&out), "a local repository needs --allow-local")
}
