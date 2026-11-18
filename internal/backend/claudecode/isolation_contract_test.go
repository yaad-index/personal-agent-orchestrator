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
	Skills  []string `json:"skills"`
	Plugins []struct {
		Path string `json:"path"`
	} `json:"plugins"`
	MemoryPaths map[string]string `json:"memory_paths"`
	answer      string
}

// reached is which categories a run reached.
func (s started) reached(canary string) map[backend.Category]bool {
	var mcp, plugins bool
	for _, srv := range s.MCPServers {
		if srv.Name != "pao" {
			mcp = true
		}
	}
	// A plugin built into Claude Code is the backend's own, not the user's.
	for _, p := range s.Plugins {
		if p.Path != "builtin" {
			plugins = true
		}
	}
	return map[backend.Category]bool{
		backend.Instructions: strings.Contains(s.answer, canary),
		backend.MCP:          mcp,
		backend.Skills:       len(s.Skills) > 0,
		backend.Settings:     plugins,
		backend.Memory:       s.MemoryPaths["auto"] != "",
	}
}

// TestContractIsolation starts the real Claude Code CLI with the adapter's
// command (ADR-0025 point 5). The node user's own environment is the one under
// test: an instruction file is planted in a directory above the working
// directory, and the user's own MCP servers, skills, settings (seen by the
// plugins they enable, other than Claude Code's built-in ones) and memory are whatever the user has. A run that
// inherits everything is the control: a category it does not reach is one this
// environment holds nothing in, and is reported and not judged. Then an agent
// that inherits nothing must reach none of them, and one that inherits one
// category must reach exactly that one. It spends a short model call per run,
// so it is opt-in:
//
//	go test -tags contract -run TestContractIsolation ./internal/backend/claudecode
func TestContractIsolation(t *testing.T) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not installed")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// Claude Code reads the instruction files above a working directory only
	// within the user's home, so the planted one is there.
	root, err := os.MkdirTemp(home, ".pao-contract-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	wd := filepath.Join(root, "agent")
	require.NoError(t, os.Mkdir(wd, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(TranscriptDir(home, wd)) })
	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	canary := "canary" + hex.EncodeToString(b)
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"),
		[]byte("The canary word is "+canary+". When asked for the canary word, answer with it.\n"), 0o600))

	run := func(t *testing.T, inherit ...string) started {
		t.Helper()
		p := core.Profile{Name: "contract", Backend: Name, Workdir: wd, Inherit: inherit, Startup: core.Startup{
			Command: shellQuote(bin) + " -p --no-session-persistence --output-format stream-json --verbose " +
				shellQuote("What is the canary word? Reply with only that word, or NONE if you were not given one."),
		}}
		require.NoError(t, backend.Table{Name: Adapter{}}.Check(p))
		cmd, err := Adapter{}.Command(p, backend.Launch{MCPServer: "pao", Hook: "true", HookTimeout: 30 * time.Second, Home: home})
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c := exec.CommandContext(ctx, "sh", "-c", cmd)
		c.Dir = wd
		c.Stdin = nil
		out, err := c.Output()
		require.NoError(t, err, "claude failed: %s", out)

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
		require.True(t, sawInit, "no init event: the stream format changed")
		return s
	}

	control := run(t, names(backend.Categories)...).reached(canary)
	require.True(t, control[backend.Instructions], "the control run did not read the planted instruction file")
	var held []backend.Category
	for _, c := range backend.Categories {
		if control[c] {
			held = append(held, c)
		} else {
			t.Logf("this user environment holds nothing in %s; it is not judged", c)
		}
	}

	t.Run("inherits nothing", func(t *testing.T) {
		got := run(t).reached(canary)
		for _, c := range held {
			assert.False(t, got[c], "reached %s", c)
		}
	})
	for _, c := range held {
		t.Run("inherits "+string(c), func(t *testing.T) {
			got := run(t, string(c)).reached(canary)
			for _, o := range held {
				assert.Equal(t, o == c, got[o], "reached %s", o)
			}
		})
	}
	assert.True(t, slices.Contains(held, backend.Instructions))
}

func names(cs []backend.Category) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}
