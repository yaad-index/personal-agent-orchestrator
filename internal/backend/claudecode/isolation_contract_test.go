//go:build contract

package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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

// started is what one run of the real Claude Code CLI loaded, as it says in its
// first event, and what it answered.
type started struct {
	MCPServers []struct {
		Name string `json:"name"`
	} `json:"mcp_servers"`
	Skills         []string          `json:"skills"`
	PermissionMode string            `json:"permissionMode"`
	MemoryPaths    map[string]string `json:"memory_paths"`
	answer         string
}

// planted is what the test puts in each category of a user environment it
// makes for the run, so every category holds something to reach.
type planted struct {
	canary string // the word in the instruction files
	mcp    string // a user-scope MCP server
	skill  string // a user skill
	mode   string // the permission mode the user's settings set
}

// reached is which categories a run reached of what was planted.
func (s started) reached(p planted) map[backend.Category]bool {
	return map[backend.Category]bool{
		backend.Instructions: strings.Contains(s.answer, p.canary),
		backend.MCP:          slices.ContainsFunc(s.MCPServers, func(m struct{ Name string `json:"name"` }) bool { return m.Name == p.mcp }),
		backend.Skills:       slices.Contains(s.Skills, p.skill),
		backend.Settings:     s.PermissionMode == p.mode,
		backend.Memory:       s.MemoryPaths["auto"] != "",
	}
}

// credentialVars are the variables the CLI takes a credential from. A hermetic
// user environment has no login of its own, so the model is asked only when one
// of them is set.
var credentialVars = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// TestContractIsolation starts the real Claude Code CLI with the adapter's
// command and environment (ADR-0025 point 5), in a user environment the test
// makes: a home directory of its own that holds something in every category.
// It holds an instruction file at user level and one above the working
// directory, a user-scope MCP server, a user skill, a user setting (a
// permission mode) and the backend's own memory. The operator's own
// environment and login play no part, so the result does not depend on them.
//
// A run that inherits everything must reach every category. A run that
// inherits nothing must reach none, and one that inherits some must reach
// exactly what want says: Claude Code reads the user's own MCP servers and
// skills along with the user's settings, so those open only with settings
// inherited too (see Adapter). Instructions are judged by the model's answer, so
// only when a credential is given in CLAUDE_CODE_OAUTH_TOKEN or
// ANTHROPIC_API_KEY. The other categories are read from the CLI's first event,
// which it writes before it needs a login. It is opt-in:
//
//	go test -tags contract -run TestContractIsolation ./internal/backend/claudecode
func TestContractIsolation(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not installed")
	}
	var credential []string
	for _, v := range credentialVars {
		if val := os.Getenv(v); val != "" {
			credential = append(credential, v+"="+val)
		}
	}
	judged := backend.Categories
	if len(credential) == 0 {
		judged = slices.DeleteFunc(slices.Clone(judged), func(c backend.Category) bool { return c == backend.Instructions })
		t.Logf("no credential in %s: instructions are not judged", strings.Join(credentialVars, " or "))
	}

	home, err := os.MkdirTemp("", "pao-contract-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	word := hex.EncodeToString(b)
	p := planted{canary: "canary" + word, mcp: "canarymcp" + word, skill: "canaryskill" + word, mode: "acceptEdits"}

	write := func(path, content string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	instruction := "The canary word is " + p.canary + ". When asked for the canary word, answer with it.\n"
	write(filepath.Join(home, ".claude", "CLAUDE.md"), instruction)
	root := filepath.Join(home, "agents")
	write(filepath.Join(root, "CLAUDE.md"), instruction)
	write(filepath.Join(home, ".claude.json"), `{"mcpServers":{"`+p.mcp+`":{"type":"stdio","command":"true"}}}`)
	write(filepath.Join(home, ".claude", "skills", p.skill, "SKILL.md"), "---\nname: "+p.skill+"\ndescription: A test skill.\n---\nSay hello.\n")
	write(filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"defaultMode":"`+p.mode+`"}}`)
	wd := filepath.Join(root, "agent")
	require.NoError(t, os.Mkdir(wd, 0o700))

	// The CLI sees the made home and nothing of the caller's own CLI or account
	// configuration, other than the credential.
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "ANTHROPIC") || strings.HasPrefix(k, "XDG_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(append(env, "HOME="+home), credential...)

	run := func(t *testing.T, inherit ...string) started {
		t.Helper()
		prof := core.Profile{Name: "contract", Backend: Name, Workdir: wd, Inherit: inherit, Startup: core.Startup{
			Command: shellQuote(bin) + " -p --no-session-persistence --output-format stream-json --verbose " +
				shellQuote("What is the canary word? Reply with only that word, or NONE if you were not given one."),
		}}
		require.NoError(t, backend.Table{Name: Adapter{}}.Check(prof))
		start, err := Adapter{}.Command(prof, backend.Launch{MCPServer: "pao", Hook: "true", HookTimeout: 30 * time.Second, Home: home})
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c := exec.CommandContext(ctx, "sh", "-c", start.Command)
		c.Env = append(slices.Clone(env), start.Env...)
		c.Dir = wd
		out, err := c.Output()
		if len(credential) > 0 {
			require.NoError(t, err, "claude failed: %s", out)
		}

		var s started
		var sawInit bool
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var e struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
				Result  string `json:"result"`
			}
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			switch {
			case e.Type == "system" && e.Subtype == "init" && !sawInit:
				require.NoError(t, json.Unmarshal(sc.Bytes(), &s))
				sawInit = true
			case e.Type == "result":
				s.answer = e.Result
			}
		}
		require.True(t, sawInit, "no init event: the stream format changed (%v)", err)
		return s
	}

	control := run(t, names(backend.Categories)...).reached(p)
	for _, c := range judged {
		require.True(t, control[c], "inheriting everything did not reach the planted %s: the signal no longer works", c)
	}

	cases := [][]string{{}}
	for _, c := range judged {
		cases = append(cases, []string{string(c)})
	}
	cases = append(cases, []string{"mcp", "settings"}, []string{"skills", "settings"})
	for _, inherit := range cases {
		name := "inherits nothing"
		if len(inherit) > 0 {
			name = "inherits " + strings.Join(inherit, "+")
		}
		t.Run(name, func(t *testing.T) {
			got := run(t, inherit...).reached(p)
			w := want(inherit)
			for _, o := range judged {
				assert.Equal(t, w[o], got[o], "reached %s", o)
			}
		})
	}
}

// want is what a run that inherits these categories reaches of what was
// planted: each category it names, except that the user's own MCP servers and
// skills come with the user's settings.
func want(inherit []string) map[backend.Category]bool {
	has := func(c backend.Category) bool { return slices.Contains(inherit, string(c)) }
	out := map[backend.Category]bool{}
	for _, c := range backend.Categories {
		out[c] = has(c)
	}
	out[backend.MCP] = has(backend.MCP) && has(backend.Settings)
	out[backend.Skills] = has(backend.Skills) && has(backend.Settings)
	return out
}

func names(cs []backend.Category) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}
