package claudecode

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Name is the backend name a profile gives to run Claude Code.
const Name = "claude-code"

// Adapter is the Claude Code backend adapter (ADR-0025). It shuts out every
// category with Claude Code's own options:
//
//   - instructions: claudeMdExcludes for the instruction files in each directory
//     above the working directory and in the user's own configuration;
//   - mcp: --strict-mcp-config, so only the configuration pao gives is read;
//   - skills: --disable-slash-commands, which turns off the profile's own skills
//     too, since Claude Code cannot tell the two apart, and CLAUDE_CODE_SYNC_SKILLS
//     off, for skills synced from the account;
//   - settings: --setting-sources project,local, so the user's settings file is
//     not read, and CLAUDE_CODE_SYNC_PLUGINS off, for plugins synced from the
//     account;
//   - memory: autoMemoryEnabled false.
//
// Claude Code reads the user's own MCP servers (in the user's configuration
// file) and the user's own skills through the same setting source as the
// user's settings. So inheriting mcp alone opens the account's connectors, and
// inheriting skills alone the skills synced from the account; the user's own
// MCP servers and skills open only when settings is inherited too. Isolating
// settings shuts them out with it: more is shut out than the profile asked,
// never less.
//
// It always gives pao's MCP configuration and pao's gate hook. The hook is
// given through --settings, which Claude Code applies whatever the setting
// sources, so shutting out the user's settings never removes the gate.
type Adapter struct{}

var _ backend.Adapter = Adapter{}

// owned are the options and environment variables the adapter sets, or that
// change what Claude Code loads (--bare and --safe-mode also drop the hook that
// is pao's gate).
var owned = []string{"--strict-mcp-config", "--mcp-config", "--disable-slash-commands", "--setting-sources", "--settings", "--bare", "--safe-mode", envSyncPlugins, envSyncSkills}

// Claude Code's switches for loading plugins and skills synced from the user's
// account. Whether they keep synced ones out has not been seen: no account they
// were tried on had any. They are set as a defensive default.
const (
	envSyncPlugins = "CLAUDE_CODE_SYNC_PLUGINS"
	envSyncSkills  = "CLAUDE_CODE_SYNC_SKILLS"
)

var ownedPattern = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(owned))
	for i, o := range owned {
		out[i] = regexp.MustCompile(`(^|[^-\w])` + regexp.QuoteMeta(o) + `($|[^-\w])`)
	}
	return out
}()

// trustPrompt is Claude Code's first-run question on a working directory it has
// not been told to trust. The cursor starts on "No, exit"; one key down is "Yes,
// I trust this folder", which Claude Code records for that directory alone.
var trustPrompt = backend.Prompt{
	About:   "trust the working directory",
	Pattern: regexp.MustCompile(`(?s)Is this a project you created or one you trust.*Yes, I trust this folder`),
	Keys:    []string{"Down", "Enter"},
}

var _ backend.Prompter = Adapter{}

// Prompts implements backend.Prompter: the question whether to trust the working
// directory. It is safe to answer for a provisioned profile too, since Command
// shuts out every setting source in a provisioned directory.
func (Adapter) Prompts(core.Profile) []backend.Prompt { return []backend.Prompt{trustPrompt} }

// Isolates implements backend.Adapter: Claude Code can shut out every category.
func (Adapter) Isolates() []backend.Category { return backend.Categories }

// Owned implements backend.Adapter. It looks for each option as a word anywhere
// in the command, so an option inside a quoted argument is reported too: a
// refusal it did not need is visible, a merge it missed is not.
func (Adapter) Owned(command string) []string {
	var out []string
	for i, re := range ownedPattern {
		if re.MatchString(command) {
			out = append(out, owned[i])
		}
	}
	return out
}

// Command implements backend.Adapter. The adapter's options follow the
// profile's command, which is the program and its own options.
func (Adapter) Command(p core.Profile, l backend.Launch) (backend.Start, error) {
	inherits, err := backend.Inherits(p)
	if err != nil {
		return backend.Start{}, err
	}
	if l.Hook == "" {
		return backend.Start{}, backend.ErrNoHook
	}
	if l.MCPServer == "" {
		return backend.Start{}, errors.New("no name for pao's MCP server")
	}
	settings := map[string]any{"hooks": hooks(l.Hook, int(l.HookTimeout.Seconds()))}
	if !inherits[backend.Instructions] {
		if l.Home == "" {
			return backend.Start{}, errors.New("no home directory to shut out the user's instructions from")
		}
		settings["claudeMdExcludes"] = Excludes(l.Home, p.Workdir)
	}
	if !inherits[backend.Memory] {
		settings["autoMemoryEnabled"] = false
	}
	sb, err := json.Marshal(settings)
	if err != nil {
		return backend.Start{}, err
	}

	var args []string
	var env []string
	if !inherits[backend.MCP] {
		args = append(args, "--strict-mcp-config")
	}
	args = append(args, "--mcp-config", MCPConfig(l.MCPServer))
	if !inherits[backend.Skills] {
		args = append(args, "--disable-slash-commands")
		env = append(env, envSyncSkills+"=0")
	}
	switch {
	case p.Provisioned && inherits[backend.Settings]:
		// A provisioned directory's settings are its source's, not the operator's
		// (ADR-0033 point 1): only the user's own, which the profile inherits.
		args = append(args, "--setting-sources", "user")
	case p.Provisioned:
		// No setting source at all: neither the user's nor any in the directory.
		args = append(args, "--setting-sources", "")
		env = append(env, envSyncPlugins+"=0")
	case !inherits[backend.Settings]:
		args = append(args, "--setting-sources", "project,local")
		env = append(env, envSyncPlugins+"=0")
	}
	args = append(args, "--settings", string(sb))

	var b strings.Builder
	b.WriteString(strings.TrimRight(p.Startup.Command, " \t"))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(a))
	}
	return backend.Start{Command: b.String(), Env: env}, nil
}

// MCPConfig is the MCP configuration that points an agent at its node's MCP
// server under the name server. It carries no secret: Claude Code fills in the
// address and the agent's token from the environment the node started it with.
func MCPConfig(server string) string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		server: map[string]any{
			"type": "http",
			"url":  "${" + backend.EnvMCPURL + "}",
			"headers": map[string]string{
				"Authorization": "Bearer ${" + backend.EnvAgentToken + "}",
			},
		},
	}})
	return string(b)
}

// Excludes are the claudeMdExcludes patterns that shut out every instruction
// file Claude Code would load from outside workdir: those in each directory
// above it, and the user's own. Claude Code matches them against absolute
// paths. Both the working directory as given and with its symbolic links
// resolved are covered, whichever Claude Code sees.
func Excludes(home, workdir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(dir string, files ...string) {
		for _, f := range files {
			p := globEscape(dir) + "/" + f
			if dir == "/" {
				p = "/" + f
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	for _, home := range variants(home) {
		add(filepath.Join(home, ".claude"), "CLAUDE*.md", "rules/**")
	}
	for _, wd := range variants(workdir) {
		for dir := filepath.Dir(wd); ; dir = filepath.Dir(dir) {
			add(dir, "CLAUDE*.md")
			add(filepath.Join(dir, ".claude"), "CLAUDE*.md", "rules/**")
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	return out
}

// variants is dir made absolute and clean, and, if different, with its symbolic
// links resolved.
func variants(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	out := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		out = append(out, real)
	}
	return out
}

// globEscape escapes the characters a glob pattern gives a meaning, so a
// directory name is matched as itself.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\*?[]{}()!+@`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// shellQuote quotes s as one word for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
