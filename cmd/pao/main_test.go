package main

import (
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.Name("pao"))
	require.NoError(t, err)

	ctx, err := parser.Parse([]string{"version"})
	require.NoError(t, err)
	assert.Equal(t, "version", ctx.Command())
}

func TestBuildVersionNotEmpty(t *testing.T) {
	assert.NotEmpty(t, buildVersion())
}

func TestConfigCheckRunsAgainstALoadableConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pao.yaml"), []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "a.yaml"), []byte("name: a\nbackend: claude-code\nstartup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\ntranscript:\n  format: claude-code\n  path: /tmp/t\n"), 0o600))

	assert.NoError(t, ConfigCheckCmd{Path: filepath.Join(dir, "pao.yaml")}.Run())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "b.yaml"), []byte("name: a\nbackend: x\nstartup:\n  command: run\n  ready_pattern: ready\n  ready_timeout: 30s\ntranscript:\n  format: claude-code\n  path: /tmp/t\n"), 0o600))
	assert.Error(t, ConfigCheckCmd{Path: filepath.Join(dir, "pao.yaml")}.Run(), "a duplicate agent name fails the check")
}

func TestConfigCheckValidatesEachChannelsAdapterOptions(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	path := filepath.Join(dir, "pao.yaml")
	base := "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nchannels:\n  - id: 1\n    name: chat\n    kind: bot\n    credential_env: PAO_CHECK_UNSET_TOKEN\n    options:\n      endpoint: https://api.example/bot{token}/{method}\n"

	require.NoError(t, os.WriteFile(path, []byte(base+"      poll_timeout: 25\n"), 0o600))
	assert.NoError(t, ConfigCheckCmd{Path: path}.Run(), "whole seconds pass, with the credential unset")

	require.NoError(t, os.WriteFile(path, []byte(base+"      poll_timeout: 25s\n"), 0o600))
	err := ConfigCheckCmd{Path: path}.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `channel "chat": option "poll_timeout" must be a whole number`)
}

// writeConfig writes a loadable orchestrator config naming PAO_RUN_TEST_TOKEN.
func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	path := filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(path, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_RUN_TEST_TOKEN\n"), 0o600))
	return path
}

func TestEveryCommandLineParsesToItsCommand(t *testing.T) {
	for line, want := range map[string]string{
		"identity invite alice":                     "identity invite <subject>",
		"identity invite alice --ttl 1h":            "identity invite <subject>",
		"identity invites":                          "identity invites",
		"identity uninvite inv-1":                   "identity uninvite <id>",
		"inbound rejected":                          "inbound rejected",
		"channel default chat bee":                  "channel default <channel> <agent>",
		"channel default chat --clear":              "channel default <channel>",
		"channel defaults":                          "channel defaults",
		"access grant agent:a --agent b":            "access grant <subject>",
		"access revoke agent:a --tag t":             "access revoke <subject>",
		"access grants --subject agent:a":           "access grants",
		"access grant operator --tool-tag research": "access grant <subject>",
	} {
		// A fresh parser per line: a process parses once, and a reused one keeps the
		// flags of the line before.
		parser, err := kong.New(&CLI{}, kong.Name("pao"))
		require.NoError(t, err)
		ctx, err := parser.Parse(strings.Fields(line))
		if assert.NoError(t, err, line) {
			assert.Equal(t, want, ctx.Command(), line)
		}
	}
}

// TestEveryRunnableCommandIsInTheTree finds each type in this package that
// has a Run method and requires it to be a command the parser can reach.
func TestEveryRunnableCommandIsInTheTree(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.Name("pao"))
	require.NoError(t, err)

	reachable := map[string]bool{}
	var walk func(*kong.Node)
	walk = func(n *kong.Node) {
		if n.Target.IsValid() {
			reachable[n.Target.Type().Name()] = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(parser.Model.Node)

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var runnable []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := goparser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Run" {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok {
				runnable = append(runnable, id.Name)
			}
		}
	}
	require.NotEmpty(t, runnable)
	for _, name := range runnable {
		assert.True(t, reachable[name], "%s has a Run method but is not in the command tree", name)
	}
}
