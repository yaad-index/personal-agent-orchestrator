package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

var launch = backend.Launch{MCPServer: "pao", Hook: "'/opt/pao it''s' hook", HookTimeout: 11 * time.Minute, Home: "/home/u"}

// launched runs the command a profile is started with through a shell, with
// printf as the program, and returns the arguments the program was given and
// the environment entries the adapter adds.
func launched(t *testing.T, p core.Profile) ([]string, []string) {
	t.Helper()
	p.Startup.Command = `printf '%s\0'`
	s, err := Adapter{}.Command(p, launch)
	require.NoError(t, err)
	out, err := exec.Command("sh", "-c", s.Command).Output()
	require.NoError(t, err, "the command does not run: %s", s.Command)
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), s.Env
}

func value(t *testing.T, args []string, opt string) string {
	t.Helper()
	i := slices.Index(args, opt)
	require.GreaterOrEqual(t, i, 0, "no %s in %q", opt, args)
	require.Less(t, i+1, len(args))
	return args[i+1]
}

type settings struct {
	Hooks struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	} `json:"hooks"`
	Excludes   []string `json:"claudeMdExcludes"`
	AutoMemory *bool    `json:"autoMemoryEnabled"`
}

func settingsOf(t *testing.T, args []string) settings {
	t.Helper()
	var s settings
	require.NoError(t, json.Unmarshal([]byte(value(t, args, "--settings")), &s))
	return s
}

func TestByDefaultTheAgentLoadsOnlyItsProfileAndPao(t *testing.T) {
	p := core.Profile{Name: "a", Backend: Name, Workdir: "/srv/agents/a"}
	args, env := launched(t, p)

	assert.Contains(t, args, "--strict-mcp-config", "mcp")
	assert.Contains(t, args, "--disable-slash-commands", "skills")
	assert.Contains(t, env, "CLAUDE_CODE_SYNC_SKILLS=0", "skills synced from the account")
	assert.Equal(t, "project,local", value(t, args, "--setting-sources"), "settings")
	assert.Contains(t, env, "CLAUDE_CODE_SYNC_PLUGINS=0", "plugins synced from the account")
	assert.Len(t, env, 2, "and nothing else")

	s := settingsOf(t, args)
	require.NotNil(t, s.AutoMemory, "memory")
	assert.False(t, *s.AutoMemory)
	assert.Equal(t, Excludes("/home/u", "/srv/agents/a"), s.Excludes, "instructions")

	require.Len(t, s.Hooks.PreToolUse, 1)
	assert.Equal(t, "*", s.Hooks.PreToolUse[0].Matcher)
	require.Len(t, s.Hooks.PreToolUse[0].Hooks, 1)
	h := s.Hooks.PreToolUse[0].Hooks[0]
	assert.Equal(t, "command", h.Type)
	assert.Equal(t, launch.Hook, h.Command, "the hook command reaches the backend as given")
	assert.Equal(t, 660, h.Timeout)

	var mcp struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(value(t, args, "--mcp-config")), &mcp))
	require.Len(t, mcp.Servers, 1)
	srv, ok := mcp.Servers["pao"]
	require.True(t, ok)
	assert.Equal(t, "http", srv.Type)
	assert.Equal(t, "${PAO_MCP_URL}", srv.URL)
	assert.Equal(t, "Bearer ${PAO_AGENT_TOKEN}", srv.Headers["Authorization"], "a reference to the environment, never a token")
}

func TestEachInheritedCategoryDropsExactlyItsOwnOption(t *testing.T) {
	base := core.Profile{Name: "a", Backend: Name, Workdir: "/srv/agents/a"}
	// Each way a category is shut out, by the category it belongs to.
	type signal struct {
		c  backend.Category
		on func(args, env []string) bool
	}
	signals := map[string]signal{
		"claudeMdExcludes":           {backend.Instructions, func(a, _ []string) bool { return settingsOf(t, a).Excludes != nil }},
		"--strict-mcp-config":        {backend.MCP, func(a, _ []string) bool { return slices.Contains(a, "--strict-mcp-config") }},
		"--disable-slash-commands":   {backend.Skills, func(a, _ []string) bool { return slices.Contains(a, "--disable-slash-commands") }},
		"CLAUDE_CODE_SYNC_SKILLS=0":  {backend.Skills, func(_, e []string) bool { return slices.Contains(e, "CLAUDE_CODE_SYNC_SKILLS=0") }},
		"--setting-sources":          {backend.Settings, func(a, _ []string) bool { return slices.Contains(a, "--setting-sources") }},
		"CLAUDE_CODE_SYNC_PLUGINS=0": {backend.Settings, func(_, e []string) bool { return slices.Contains(e, "CLAUDE_CODE_SYNC_PLUGINS=0") }},
		"autoMemoryEnabled":          {backend.Memory, func(a, _ []string) bool { return settingsOf(t, a).AutoMemory != nil }},
	}
	for _, c := range backend.Categories {
		p := base
		p.Inherit = []string{string(c)}
		args, env := launched(t, p)
		for name, s := range signals {
			assert.Equal(t, s.c != c, s.on(args, env), "inherit %s: is %s set", c, name)
		}
	}

	p := base
	for _, c := range backend.Categories {
		p.Inherit = append(p.Inherit, string(c))
	}
	args, env := launched(t, p)
	for name, s := range signals {
		assert.False(t, s.on(args, env), "inheriting everything still sets %s", name)
	}
	assert.NotEmpty(t, value(t, args, "--mcp-config"), "pao's MCP configuration is given whatever is inherited")
	assert.Len(t, settingsOf(t, args).Hooks.PreToolUse, 1, "and pao's gate hook")
}

func TestTheAdapterRefusesToStartWithoutAGateHook(t *testing.T) {
	p := core.Profile{Name: "a", Backend: Name, Workdir: "/w", Startup: core.Startup{Command: "claude"}}
	l := launch
	l.Hook = ""
	_, err := Adapter{}.Command(p, l)
	assert.ErrorIs(t, err, backend.ErrNoHook)
}

func TestTheOptionsTheAdapterOwnsAreFoundAsWords(t *testing.T) {
	a := Adapter{}
	for _, o := range owned {
		assert.Equal(t, []string{o}, a.Owned("claude --model x "+o+" v"), o)
		assert.Equal(t, []string{o}, a.Owned("claude "+o+"=v"), o+"=")
		assert.Equal(t, []string{o}, a.Owned(`claude "`+o+`" v`), "quoted "+o)
	}
	assert.Empty(t, a.Owned("claude --model m --append-system-prompt 'be brief'"))
	assert.Equal(t, []string{"--setting-sources"}, a.Owned("claude --setting-sources user"), "--settings is not --setting-sources")
	assert.Equal(t, []string{"--strict-mcp-config"}, a.Owned("claude --strict-mcp-config"), "--mcp-config is not --strict-mcp-config")
	assert.Empty(t, a.Owned("claude --settingsx --mcp-configs"))
	assert.Equal(t, []string{"CLAUDE_CODE_SYNC_PLUGINS"}, a.Owned("CLAUDE_CODE_SYNC_PLUGINS=1 claude"), "a switch the adapter sets, set by the profile")
	assert.Equal(t, []string{"CLAUDE_CODE_SYNC_SKILLS"}, a.Owned("env CLAUDE_CODE_SYNC_SKILLS=1 claude"))
	assert.Empty(t, a.Owned("CLAUDE_CODE_SYNC_PLUGINS_MCP_TIMEOUT_MS=9 claude"), "a longer name is another variable")
}

func TestExcludesCoverEveryDirectoryAboveTheWorkdirAndTheUsersOwn(t *testing.T) {
	got := Excludes("/home/u", "/home/u/agents/a")
	assert.Equal(t, []string{
		"/home/u/.claude/CLAUDE*.md", "/home/u/.claude/rules/**",
		"/home/u/agents/CLAUDE*.md", "/home/u/agents/.claude/CLAUDE*.md", "/home/u/agents/.claude/rules/**",
		"/home/u/CLAUDE*.md",
		"/home/CLAUDE*.md", "/home/.claude/CLAUDE*.md", "/home/.claude/rules/**",
		"/CLAUDE*.md", "/.claude/CLAUDE*.md", "/.claude/rules/**",
	}, got)
	for _, p := range got {
		assert.False(t, strings.HasPrefix(p, "/home/u/agents/a/"), "the workdir's own %s is not shut out", p)
	}
}

func TestExcludesCoverTheWorkdirThroughASymlinkAndEscapeGlobCharacters(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	real := filepath.Join(root, "re[al]", "a")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(filepath.Join(root, "re[al]"), link))

	got := Excludes("/nohome", filepath.Join(link, "a"))
	assert.Contains(t, got, globEscape(link)+"/CLAUDE*.md", "the path as given")
	assert.Contains(t, got, globEscape(filepath.Join(root, "re[al]"))+"/CLAUDE*.md", "the path with its links resolved")
	assert.Contains(t, got, globEscape(root)+`/re\[al\]/CLAUDE*.md`)
}

// trustPane is Claude Code's first-run question as a node's pane showed it.
const trustPane = `────────────────────────────────────────────────────────────────────────────────
 Accessing workspace:

 /srv/agents/governor

 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Trusting this folder loads .mcp.json here or above (MCP servers).
 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`

func TestTheTrustQuestionIsAnsweredWithYes(t *testing.T) {
	ps := Adapter{}.Prompts()
	require.Len(t, ps, 1)
	assert.True(t, ps[0].Pattern.MatchString(trustPane), "the question as Claude Code shows it")
	assert.Equal(t, []string{"Down", "Enter"}, ps[0].Keys, "from No, exit down to Yes, then confirm")
	assert.False(t, ps[0].Pattern.MatchString("❯ Try \"refactor <filepath>\"\n  ⏵⏵ auto mode on"), "not the ready prompt")
	assert.False(t, ps[0].Pattern.MatchString("[alice]: Is this a project you created or one you trust?"), "not the question alone, without its answer")
}
