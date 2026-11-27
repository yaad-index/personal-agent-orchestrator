package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// onlyTag lets the operator reach the agents carrying tag, and nothing else.
type onlyTag string

func (o onlyTag) Allow(s access.Subject, _ access.Action, r access.Resource) bool {
	if s.Kind != access.KindOperator {
		return false
	}
	for _, t := range r.Tags {
		if t == string(o) {
			return true
		}
	}
	return false
}

func workdirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	wd := t.TempDir()
	for p, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(wd, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wd, p), []byte(body), 0o644))
	}
	return wd
}

func TestTheFileCommandReadsOnlyWhatTheOperatorMayHave(t *testing.T) {
	vault := workdirWith(t, map[string]string{"notes/a.md": "note", ".git/config": "secret"})
	other := workdirWith(t, map[string]string{"notes/a.md": "other's note"})
	profiles := map[string]core.Profile{
		"vault": {Name: "vault", Tags: []string{"home"}, Workdir: vault},
		"other": {Name: "other", Tags: []string{"elsewhere"}, Workdir: other},
	}
	c := chatCommands{auth: onlyTag("home"), profiles: func(n string) (core.Profile, core.State, bool) {
		p, ok := profiles[n]
		return p, core.Running, ok
	}}

	f, err := c.File(context.Background(), "vault", "notes/a.md")
	require.NoError(t, err)
	assert.Equal(t, "a.md", f.Name)
	assert.Equal(t, "note", string(f.Data))

	refusal := func(agent, path string) string {
		_, err := c.File(context.Background(), agent, path)
		require.Error(t, err)
		return strings.ReplaceAll(strings.ReplaceAll(err.Error(), agent, "AGENT"), path, "PATH")
	}
	want := refusal("vault", "missing.md")
	for _, tc := range [][2]string{{"vault", ".git/config"}, {"vault", "../x"}, {"other", "notes/a.md"}, {"nobody", "notes/a.md"}} {
		assert.Equal(t, want, refusal(tc[0], tc[1]), "%v: one refusal for missing, forbidden, out of reach and absent", tc)
	}

	big := workdirWith(t, map[string]string{"big.bin": strings.Repeat("x", maxFile+1)})
	profiles["vault"] = core.Profile{Name: "vault", Tags: []string{"home"}, Workdir: big}
	_, err = c.File(context.Background(), "vault", "big.bin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")
}

func TestAnAgentsLinksToItsOwnFilesBecomeButtons(t *testing.T) {
	wd := workdirWith(t, map[string]string{"notes/a.md": "a", "notes/b c.md": "b", ".git/config": "x"})
	p := core.Profile{Name: "vault", Workdir: wd}
	text := "See [a](notes/a.md), [b](notes/b%20c.md), [again](notes/a.md#x), [gone](notes/missing.md), " +
		"[git](.git/config), [up](../etc/passwd), [web](https://e.example/notes/a.md)."
	got := fileActions(p, text)
	assert.Equal(t, []core.Action{
		{Label: "📎 a.md", Command: "/pao file vault notes/a.md"},
		{Label: "📎 b c.md", Command: "/pao file vault notes/b c.md"},
	}, got, "one button per file that may be sent, each once")

	var many strings.Builder
	for i := range 8 {
		name := filepath.Join("n", string(rune('a'+i))+".md")
		require.NoError(t, os.MkdirAll(filepath.Join(wd, "n"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wd, name), []byte("x"), 0o644))
		many.WriteString("[x](" + name + ") ")
	}
	assert.Len(t, fileActions(p, many.String()), maxFileButtons)

	m, ok := entryMessage(p, core.Entry{Kind: core.KindMessage, Role: "assistant", Text: "[a](notes/a.md)"}, core.DefaultLevels(), 1)
	require.True(t, ok)
	assert.Len(t, m.Actions, 1, "the agent's own words carry the buttons")
	m, ok = entryMessage(p, core.Entry{Kind: core.KindToolResult, Text: "[a](notes/a.md)"}, core.DefaultLevels(), 4)
	require.True(t, ok)
	assert.Empty(t, m.Actions, "pao's rendering of a tool result carries none")
}

func TestTheTerminalChannelSaysItCannotCarryFiles(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	require.NoError(t, os.WriteFile(filepath.Join(n.dir, "note.md"), []byte("x"), 0o644))
	got, err := n.client.Post(context.Background(), "human", "/pao file a note.md", "name", "a")
	require.NoError(t, err)
	assert.Equal(t, comm.ReplyNoFiles, got.Reply)
}
