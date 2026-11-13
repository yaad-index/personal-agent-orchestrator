package recipe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validRecipe = `name: researcher
description: reads and reports
profile:
  backend: harness
  tags: [research]
  startup:
    command: "run-agent --name {name} --tools {tools} --dir {workdir}"
    ready_pattern: "READY>"
    ready_timeout: 30s
  transcript:
    format: harness-jsonl
workdir:
  files:
    README.md: hello
tools: [Read, Grep]
`

func writeRecipe(t *testing.T, dir, name, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, name+".yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func parse(t *testing.T, content string) (Recipe, error) {
	t.Helper()
	return Parse(writeRecipe(t, t.TempDir(), "researcher", content))
}

func locate(format, wd string) (string, bool) {
	if format == "harness-jsonl" {
		return filepath.Join(wd, ".transcripts"), true
	}
	return "", false
}

func TestParseAValidRecipe(t *testing.T) {
	r, err := parse(t, validRecipe)
	require.NoError(t, err)
	assert.Equal(t, "researcher", r.Name)
	assert.Equal(t, []string{"Read", "Grep"}, r.Tools)
	assert.Equal(t, []string{"research"}, r.Profile.Tags)
	src := r.Source()
	assert.Equal(t, map[string][]byte{"README.md": []byte("hello")}, src.Files)
	assert.Nil(t, src.Git)
}

func TestInstantiateFillsNameWorkdirAndTranscript(t *testing.T) {
	r, err := parse(t, validRecipe)
	require.NoError(t, err)

	p, err := r.Instantiate("tmp-a", "/srv/pao/temp/tmp-a", locate)
	require.NoError(t, err)

	assert.Equal(t, "tmp-a", p.Name)
	assert.Equal(t, "/srv/pao/temp/tmp-a", p.Workdir)
	assert.Equal(t, "/srv/pao/temp/tmp-a/.transcripts", p.Transcript.Path, "an empty transcript path is derived from the workdir by the backend")
	assert.Equal(t, "run-agent --name 'tmp-a' --tools 'Read,Grep' --dir '/srv/pao/temp/tmp-a'", p.Startup.Command)
	assert.Equal(t, r.Profile.Tags, p.Tags)
	assert.Empty(t, r.Profile.Name, "the recipe itself is not changed by instantiating it")
}

func TestInstantiateDoesNotShareTagsWithTheRecipe(t *testing.T) {
	r, err := parse(t, validRecipe)
	require.NoError(t, err)
	p, err := r.Instantiate("a", "/w", locate)
	require.NoError(t, err)
	p.Tags[0] = "mutated"
	assert.Equal(t, []string{"research"}, r.Profile.Tags)
}

func sh(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", command).Output()
	require.NoError(t, err)
	return string(out)
}

func TestSubstitutedValuesReachTheShellAsOneLiteralWord(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	nasty := "it's a dir; touch " + marker + " #"
	r, err := parse(t, strings.Replace(validRecipe, `run-agent --name {name} --tools {tools} --dir {workdir}`, `printf '%s|%s|%s' {name} {tools} {workdir}`, 1))
	require.NoError(t, err)
	r.Tools = []string{"Bash(git diff:*)", "x'; touch " + marker + "; '"}

	p, err := r.Instantiate("tmp-a", nasty, locate)
	require.NoError(t, err)

	got := sh(t, p.Startup.Command)
	assert.Equal(t, "tmp-a|Bash(git diff:*),x'; touch "+marker+"; '|"+nasty, got, "every value arrives as a single literal word")
	assert.NoFileExists(t, marker, "nothing in a value is run by the shell")
}

func TestShellVariablesAreLeftAlone(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, `run-agent --name {name} --tools {tools} --dir {workdir}`, `echo ${HOME} ${name} {name}`, 1))
	require.NoError(t, err)
	p, err := r.Instantiate("a", "/w", locate)
	require.NoError(t, err)
	assert.Equal(t, "echo ${HOME} ${name} 'a'", p.Startup.Command, "a shell variable, even one named like a placeholder, is not a placeholder")
}

func TestAdjacentPlaceholdersAreAllSubstituted(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, `run-agent --name {name} --tools {tools} --dir {workdir}`, `x{name}{workdir}{tools}y`, 1))
	require.NoError(t, err)
	p, err := r.Instantiate("a", "/w", locate)
	require.NoError(t, err)
	assert.Equal(t, "x'a''/w''Read,Grep'y", p.Startup.Command)
}

func TestSubstitutionIsSinglePass(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, `run-agent --name {name} --tools {tools} --dir {workdir}`, `echo {tools} {name}`, 1))
	require.NoError(t, err)
	r.Tools = []string{"{name}"}
	p, err := r.Instantiate("a", "/w", locate)
	require.NoError(t, err)
	assert.Equal(t, "echo '{name}' 'a'", p.Startup.Command, "a value that looks like a placeholder is not expanded again")
}

func TestTranscriptPathMayUsePlaceholdersUnquoted(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, "    format: harness-jsonl", "    format: harness-jsonl\n    path: \"{workdir}/out-{name}\"", 1))
	require.NoError(t, err)
	p, err := r.Instantiate("a", "/w x", nil)
	require.NoError(t, err)
	assert.Equal(t, "/w x/out-a", p.Transcript.Path, "a path is not a shell word, so it is not quoted")
}

func TestNoTranscriptPathAndNoLocatorFailsAtSpawn(t *testing.T) {
	r, err := parse(t, validRecipe)
	require.NoError(t, err, "the recipe is fine; whether a backend can locate its transcript is known at spawn")
	_, err = r.Instantiate("a", "/w", nil)
	require.Error(t, err)
	_, err = r.Instantiate("a", "/w", func(string, string) (string, bool) { return "", false })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness-jsonl")
}

func TestInstantiateHoldsTheProfileToTheUsualRules(t *testing.T) {
	r, err := parse(t, validRecipe)
	require.NoError(t, err)
	_, err = r.Instantiate("Bad Name", "/w", locate)
	assert.Error(t, err, "an invalid agent name")
	r.Profile.Startup.ReadyPattern = "("
	_, err = r.Instantiate("ok", "/w", locate)
	assert.Error(t, err, "an invalid ready pattern")
}

func TestRecipeValidationAtLoad(t *testing.T) {
	cases := map[string]string{
		"bad name":            strings.Replace(validRecipe, "name: researcher", "name: Researcher", 1),
		"name vs file":        strings.Replace(validRecipe, "name: researcher", "name: other", 1),
		"profile name set":    strings.Replace(validRecipe, "  backend: harness", "  name: fixed\n  backend: harness", 1),
		"profile workdir":     strings.Replace(validRecipe, "  backend: harness", "  workdir: /tmp/x\n  backend: harness", 1),
		"unknown key":         validRecipe + "extra: 1\n",
		"unknown field":       strings.Replace(validRecipe, "description:", "descr1ption:", 1),
		"unknown placeholder": strings.Replace(validRecipe, "{workdir}", "{workdri}", 1),
		"no backend":          strings.Replace(validRecipe, "  backend: harness\n", "", 1),
		"no source":           strings.Replace(validRecipe, "workdir:\n  files:\n    README.md: hello\n", "", 1),
		"two sources":         strings.Replace(validRecipe, "workdir:\n", "workdir:\n  git:\n    url: https://example.com/x.git\n", 1),
		"bad file path":       strings.Replace(validRecipe, "README.md: hello", "../escape: hello", 1),
		"dash git url":        strings.Replace(strings.Replace(validRecipe, "  files:\n    README.md: hello\n", "  git:\n    url: --upload-pack=x\n", 1), "x", "x", 1),
		"empty tool":          strings.Replace(validRecipe, "tools: [Read, Grep]", `tools: ["Read", ""]`, 1),
		"tool with comma":     strings.Replace(validRecipe, "tools: [Read, Grep]", `tools: ["Read,Grep"]`, 1),
		"tool with newline":   strings.Replace(validRecipe, "tools: [Read, Grep]", `tools: ["Read\nGrep"]`, 1),
	}
	for name, content := range cases {
		_, err := parse(t, content)
		assert.Error(t, err, name)
	}
}

func TestEmptyFileSetIsAValidWorkdir(t *testing.T) {
	_, err := parse(t, strings.Replace(validRecipe, "  files:\n    README.md: hello\n", "  files: {}\n", 1))
	require.NoError(t, err)
}

func TestGitSourceIsParsed(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, "  files:\n    README.md: hello\n", "  git:\n    url: https://example.com/x.git\n    ref: v1\n", 1))
	require.NoError(t, err)
	src := r.Source()
	require.NotNil(t, src.Git)
	assert.Equal(t, "https://example.com/x.git", src.Git.URL)
	assert.Equal(t, "v1", src.Git.Ref)
	assert.Nil(t, src.Files)
}

func TestLoadDirFindsLooseRecipesAndSets(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, "researcher", validRecipe)
	writeRecipe(t, filepath.Join(dir, "team"), "writer", strings.Replace(validRecipe, "name: researcher", "name: writer", 1))
	writeRecipe(t, filepath.Join(dir, ".hidden"), "ghost", strings.Replace(validRecipe, "name: researcher", "name: ghost", 1))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "team", "sub"), 0o700))
	writeRecipe(t, filepath.Join(dir, "team", "sub"), "deep", strings.Replace(validRecipe, "name: researcher", "name: deep", 1))

	s, err := LoadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"researcher", "writer"}, s.Names(), "loose, and one level into a set; not hidden dirs, not deeper")
	assert.Equal(t, filepath.Join(dir, "team", "writer.yaml"), s.Path("writer"))
	r, ok := s.Get("writer")
	require.True(t, ok)
	assert.Equal(t, "writer", r.Name)
	_, ok = s.Get("ghost")
	assert.False(t, ok)
}

func TestLoadDirOnAMissingDirIsEmpty(t *testing.T) {
	s, err := LoadDir(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	assert.Empty(t, s.Names())
}

func TestOneBadRecipeFailsTheWholeLoad(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, "researcher", validRecipe)
	writeRecipe(t, dir, "broken", "name: broken\n")
	_, err := LoadDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken.yaml")
}

func TestADuplicateNameAcrossSetsFailsTheLoad(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, filepath.Join(dir, "a"), "researcher", validRecipe)
	writeRecipe(t, filepath.Join(dir, "b"), "researcher", validRecipe)
	_, err := LoadDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already defined")
}

func TestInstantiateDoesNotShareLevelsWithTheRecipe(t *testing.T) {
	r, err := parse(t, strings.Replace(validRecipe, "  transcript:\n", "  levels:\n    message: 2\n  transcript:\n", 1))
	require.NoError(t, err)
	p, err := r.Instantiate("a", "/w", locate)
	require.NoError(t, err)
	for k := range p.Levels {
		p.Levels[k] = 9
	}
	for _, v := range r.Profile.Levels {
		assert.NotEqual(t, 9, v)
	}
}
