package builtin

import (
	"context"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type noHandler struct{}

func (noHandler) ReceiveFromChannel(context.Context, comm.ChannelInbound) (comm.ChannelResult, error) {
	return comm.ChannelResult{}, nil
}
func (noHandler) Destinations(string, core.Message) []string   { return nil }
func (noHandler) NativeOf(string, string) (string, bool)       { return "", false }
func (noHandler) DefaultAgent(string) (string, bool)           { return "", false }
func (noHandler) DefaultAgentIn(string, string) (string, bool) { return "", false }

func TestTheBuildContainsTheBotAdapterAndCanBuildOne(t *testing.T) {
	tb := Table()
	assert.Equal(t, []string{"bot"}, tb.Kinds())
	as, err := tb.Build([]adapter.Spec{{Name: "tg", Kind: "bot", Credential: "x", Options: map[string]any{"endpoint": "https://api.example/bot{token}/{method}"}, Channels: []string{"chat", "team"}}}, noHandler{})
	require.NoError(t, err)
	require.Len(t, as, 1)
	require.Len(t, as[0].Channels(), 2)
	assert.Equal(t, "chat", as[0].Channels()[0].Name())
	assert.Equal(t, "team", as[0].Channels()[1].Name())
}

func TestABotWithoutItsEndpointStopsTheBuild(t *testing.T) {
	_, err := Table().Build([]adapter.Spec{{Name: "tg", Kind: "bot", Credential: "x", Channels: []string{"chat"}}}, noHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")
}

func TestEveryKindCanCheckItsOptionsWithoutACredential(t *testing.T) {
	for kind, k := range Table() {
		assert.NotNil(t, k.Check, kind)
	}
	err := Table().Check([]adapter.Spec{{ID: 1, Name: "chat", Kind: "bot", Options: map[string]any{"endpoint": "https://api.example/bot{token}/{method}", "poll_timeout": "25s"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "chat": option "poll_timeout" must be a whole number`)
}

// managedNode writes a node whose pao.yaml names a managed file, with the
// providers mine, which carries a channel, and ui-made, which carries none,
// plus extra.
func managedNode(t *testing.T, extra, managed string) (pao, mf string) {
	t.Helper()
	dir := t.TempDir()
	pao, mf = filepath.Join(dir, "pao.yaml"), filepath.Join(dir, "managed.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(pao, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nmanaged: managed.yaml\n"+
		"providers:\n  - name: mine\n    kind: bot\n    credential_env: PAO_BOT\n    options:\n      endpoint: \"https://bot.example/bot{token}/{method}\"\n"+
		"  - name: ui-made\n    kind: bot\n    credential_env: PAO_UI\n    options:\n      endpoint: \"https://bot.example/bot{token}/{method}\"\n"+extra+
		"channels:\n  - id: 1\n    name: mine\n    provider: mine\n    owner: alice\n    target: private\n"), 0o600))
	if managed != "" {
		require.NoError(t, os.WriteFile(mf, []byte(managed), 0o600))
	}
	return pao, mf
}

func TestAProvidersAdapterErrorNamesPaoYaml(t *testing.T) {
	pao, _ := managedNode(t, "  - name: odd\n    kind: bot\n    credential_env: PAO_ODD\n    options:\n      nonsense: 1\n", "")
	cfg, err := config.Load(pao)
	require.NoError(t, err, "the options are the adapter's to judge, not the loader's")
	err = CheckChannels(pao, cfg.Orchestrator)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), pao+": "), "a provider is defined in pao.yaml only: %s", err)
	assert.Contains(t, err.Error(), `provider "odd"`)
}

func TestAWriteThatWouldNotStartIsNotWritten(t *testing.T) {
	pao, mf := managedNode(t, "", "")
	check := func(cfg *config.Config) error { return CheckChannels(pao, cfg.Orchestrator) }
	bad := config.Managed{Channels: []config.Channel{{ID: 2, Name: "ui-made", Provider: "nope", Owner: "alice", Target: config.TargetPrivate}}}
	require.Error(t, config.WriteManaged(pao, bad, check), "a provider the configuration does not define")
	_, err := os.Stat(mf)
	assert.True(t, os.IsNotExist(err), "nothing was written")

	good := config.Managed{Channels: []config.Channel{{ID: 2, Name: "ui-made", Provider: "ui-made", Owner: "alice", Target: config.TargetPrivate}}}
	require.NoError(t, config.WriteManaged(pao, good, check))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	require.NoError(t, CheckChannels(pao, cfg.Orchestrator), "what was written starts")
}
