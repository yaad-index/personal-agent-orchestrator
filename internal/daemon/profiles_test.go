package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

func profileText(name, extra string) string {
	return "# hand-written, kept as it is\nname: " + name + "\ntags: [home]\nbackend: claude-code\n" +
		"inherit: [instructions, mcp, skills, settings, memory]\n" +
		"startup:\n  command: run\n  ready_pattern: READY\n  ready_timeout: 5s\n" +
		"transcript:\n  format: claude-code\n  path: /tmp/t\n" + extra
}

func profileSetup(t *testing.T) (*profileFiles, *registry.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	reg := registry.New()
	return newProfileFiles(dir, config.Orchestrator{}, reg, checkProfile), reg, dir
}

func TestANewProfileIsWrittenAsGivenAndBecomesAnAgentAtOnce(t *testing.T) {
	f, reg, dir := profileSetup(t)
	text := profileText("notes", "")
	got, err := f.Create(text)
	require.NoError(t, err)
	assert.Equal(t, local.ProfileJSON{File: "notes.yaml", Name: "notes", Backend: "claude-code", Tags: []string{"home"}, Status: local.ProfileLoaded}, got)

	b, err := os.ReadFile(filepath.Join(dir, "notes.yaml"))
	require.NoError(t, err)
	assert.Equal(t, text, string(b), "the text as written, comment and all")
	p, state, ok := reg.Lookup("notes")
	require.True(t, ok, "the node knows the agent without a restart")
	assert.Equal(t, core.Stopped, state)
	assert.Equal(t, []string{"home"}, p.Tags)

	_, err = f.Create(text)
	assert.ErrorIs(t, err, local.ErrExists, "a second profile of the same name")
}

func TestAnInvalidProfileIsRefusedAndNothingIsWritten(t *testing.T) {
	f, reg, dir := profileSetup(t)
	for _, text := range []string{"name: x\n", "not: [yaml", profileText("x", "unknown_key: 1\n"), profileText("Bad Name", "")} {
		_, err := f.Create(text)
		assert.ErrorIs(t, err, local.ErrInvalid, text)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Empty(t, reg.All())
}

func TestAStoppedAgentTakesItsSavedProfileAtOnce(t *testing.T) {
	f, reg, _ := profileSetup(t)
	_, err := f.Create(profileText("notes", ""))
	require.NoError(t, err)
	got, err := f.Save("notes.yaml", strings.Replace(profileText("notes", ""), "tags: [home]", "tags: [home, writing]", 1))
	require.NoError(t, err)
	assert.Equal(t, local.ProfileLoaded, got.Status)
	p, _, _ := reg.Lookup("notes")
	assert.Equal(t, []string{"home", "writing"}, p.Tags)
}

func TestARunningAgentKeepsItsProfileUntilItStops(t *testing.T) {
	f, reg, _ := profileSetup(t)
	_, err := f.Create(profileText("notes", ""))
	require.NoError(t, err)
	require.NoError(t, reg.SetState("notes", core.Running))

	got, err := f.Save("notes.yaml", strings.Replace(profileText("notes", ""), "tags: [home]", "tags: [writing]", 1))
	require.NoError(t, err)
	assert.Equal(t, local.ProfileRestart, got.Status)
	assert.True(t, got.Running)
	p, _, _ := reg.Lookup("notes")
	assert.Equal(t, []string{"home"}, p.Tags, "the running agent's profile is untouched")

	list, err := f.List()
	require.NoError(t, err)
	assert.Equal(t, local.ProfileRestart, list[0].Status)

	next, ok := f.takePending("notes")
	require.True(t, ok)
	assert.Equal(t, []string{"writing"}, next.Tags)
	_, ok = f.takePending("notes")
	assert.False(t, ok, "handed over once")
}

func TestAProfileKeepsItsAgentsName(t *testing.T) {
	f, _, _ := profileSetup(t)
	_, err := f.Create(profileText("notes", ""))
	require.NoError(t, err)
	_, err = f.Save("notes.yaml", profileText("other", ""))
	assert.ErrorIs(t, err, local.ErrInvalid)
	assert.Contains(t, err.Error(), "a new name is a new profile")
}

func TestOnlyAPlainProfileFileCanBeAddressed(t *testing.T) {
	f, _, dir := profileSetup(t)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.yaml"), []byte(profileText("o", "")), 0o644))
	for _, file := range []string{"../outside.yaml", "notes", ".hidden.yaml", "a/b.yaml", "missing.yaml"} {
		_, err := f.Get(file)
		assert.ErrorIs(t, err, local.ErrNotFound, file)
		_, err = f.Save(file, profileText("notes", ""))
		assert.ErrorIs(t, err, local.ErrNotFound, file)
	}
}

func TestTheListSaysWhatTheNodeRunsOfEachFile(t *testing.T) {
	f, reg, dir := profileSetup(t)
	_, err := f.Create(profileText("loaded", ""))
	require.NoError(t, err)
	// written by hand after the node started
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handmade.yaml"), []byte(profileText("handmade", "")), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: [oops"), 0o644))
	_ = reg

	list, err := f.List()
	require.NoError(t, err)
	require.Len(t, list, 3)
	by := map[string]local.ProfileJSON{}
	for _, p := range list {
		by[p.File] = p
	}
	assert.Equal(t, local.ProfileLoaded, by["loaded.yaml"].Status)
	assert.Equal(t, local.ProfileNotApplied, by["handmade.yaml"].Status, "a file the node has not taken")
	assert.Equal(t, local.ProfileInvalid, by["broken.yaml"].Status)
	assert.NotEmpty(t, by["broken.yaml"].Error)
	assert.Empty(t, by["broken.yaml"].Name)

	got, err := f.Get("handmade.yaml")
	require.NoError(t, err)
	assert.Equal(t, profileText("handmade", ""), got.Text)
	_, err = f.Save("handmade.yaml", got.Text)
	require.NoError(t, err)
	_, _, ok := reg.Lookup("handmade")
	assert.True(t, ok, "saving a hand-made file applies it")

	_, err = f.Save("broken.yaml", profileText("loaded", ""))
	assert.ErrorIs(t, err, local.ErrExists, "a fixed file cannot take another file's agent")
	_, err = f.Save("broken.yaml", profileText("fixed", ""))
	require.NoError(t, err, "an invalid file can be fixed")
}

func TestAProfileTheNodeCannotRunIsRefused(t *testing.T) {
	f, _, _ := profileSetup(t)
	_, err := f.Create(strings.Replace(profileText("x", ""), "format: claude-code", "format: nope", 1))
	assert.ErrorIs(t, err, local.ErrInvalid, "a transcript format the node cannot read")
}

func TestATemporaryAgentsNameIsTaken(t *testing.T) {
	f, reg, _ := profileSetup(t)
	require.NoError(t, reg.AddTemp(core.Profile{Name: "tmp-a"}, registry.TempInfo{}))
	_, err := f.Create(profileText("tmp-a", ""))
	assert.ErrorIs(t, err, local.ErrExists)
}

func TestAProfileCreatedOnTheNodeRunsAndAnEditAppliesAtItsRestart(t *testing.T) {
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, _ string, cfg *config.Config) {
		cfg.Orchestrator.ProfilesDir = filepath.Join(dir, "profiles")
		require.NoError(t, os.MkdirAll(cfg.Orchestrator.ProfilesDir, 0o755))
	})
	ctx := context.Background()
	text := "name: b\ntags: [team]\nbackend: claude-code\nworkdir: " + n.dir + "\n" +
		"startup:\n  command: \"env TRANSCRIPT=" + filepath.Join(n.dir, "transcripts", "b.jsonl") + " sh " + n.scriptPath + "\"\n" +
		"  ready_pattern: READY>\n  ready_timeout: 700ms\n" +
		"transcript:\n  format: claude-code\n  path: " + filepath.Join(n.dir, "transcripts", "b.jsonl") + "\n"
	created, err := n.client.CreateProfile(ctx, text)
	require.NoError(t, err)
	assert.Equal(t, local.ProfileLoaded, created.Status)

	_, err = n.client.Post(ctx, "human", "hi", "name", "b")
	require.NoError(t, err, "the new agent is addressable without restarting pao")
	assert.Equal(t, "echo: [human]: hi", n.next(t).Text)

	saved, err := n.client.SaveProfile(ctx, "b.yaml", strings.Replace(text, "tags: [team]", "tags: [team, later]", 1))
	require.NoError(t, err)
	assert.Equal(t, local.ProfileRestart, saved.Status)
	require.NoError(t, n.client.AgentAction(ctx, "b", "restart"))
	list, err := n.client.Profiles(ctx)
	require.NoError(t, err)
	for _, p := range list {
		if p.Name == "b" {
			assert.Equal(t, local.ProfileLoaded, p.Status, "the restart applied it")
			assert.Equal(t, []string{"team", "later"}, p.Tags)
		}
	}
}

func TestADraftIsAProfileTheNodeWouldLoadAndNothingIsWritten(t *testing.T) {
	f, reg, dir := profileSetup(t)
	text, err := f.Draft(local.ProfileDraftJSON{Name: "notes", Workdir: "/srv/agents/notes", Model: "sonnet", Tags: []string{"home", "writing"}})
	require.NoError(t, err)
	p, err := config.ParseProfile([]byte(text), config.Orchestrator{})
	require.NoError(t, err, text)
	assert.Equal(t, "notes", p.Name)
	assert.Equal(t, []string{"home", "writing"}, p.Tags)
	assert.Equal(t, "/srv/agents/notes", p.Workdir)
	assert.Equal(t, "claude --model sonnet", p.Startup.Command)
	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".claude", "projects", "-srv-agents-notes"), p.Transcript.Path, "where Claude Code keeps that directory's transcripts")
	assert.Contains(t, text, "# notes: an agent run with Claude Code.")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
	assert.Empty(t, reg.All())

	for name, d := range map[string]local.ProfileDraftJSON{
		"bad name":     {Name: "Notes", Workdir: "/srv/x"},
		"relative dir": {Name: "n", Workdir: "srv/x"},
		"unclean dir":  {Name: "n", Workdir: "/srv/../x"},
		"bad model":    {Name: "n", Workdir: "/srv/x", Model: "sonnet; rm -rf"},
		"bad tag":      {Name: "n", Workdir: "/srv/x", Tags: []string{"Has Space"}},
	} {
		_, err := f.Draft(d)
		assert.ErrorIs(t, err, local.ErrInvalid, name)
	}
}

func TestCreateNeverOverwritesAFile(t *testing.T) {
	f, _, dir := profileSetup(t)
	// notes.yaml holds another agent, written by hand
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.yaml"), []byte(profileText("other", "")), 0o644))
	_, err := f.Create(profileText("notes", ""))
	assert.ErrorIs(t, err, local.ErrExists)
	b, err := os.ReadFile(filepath.Join(dir, "notes.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "name: other", "the file is as it was")
}

func TestAConfiguredAgentIsOneOfTheProfilesNeverATemporaryOne(t *testing.T) {
	f, reg, _ := profileSetup(t)
	configured := configuredIn(reg)
	_, err := f.Create(profileText("notes", ""))
	require.NoError(t, err)
	require.NoError(t, reg.AddTemp(core.Profile{Name: "tmp-a"}, registry.TempInfo{}))
	assert.True(t, configured("notes"), "a profile added after start")
	assert.False(t, configured("tmp-a"))
	assert.False(t, configured("nobody"))
}
