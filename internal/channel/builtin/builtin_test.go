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
	as, err := tb.Build([]adapter.Spec{{ID: 1, Name: "chat", Kind: "bot", Credential: "x", Options: map[string]any{"endpoint": "https://api.example/bot{token}/{method}"}}}, noHandler{})
	require.NoError(t, err)
	require.Len(t, as, 1)
	assert.Equal(t, "chat", as[0].Name())
}

func TestABotWithoutItsEndpointStopsTheBuild(t *testing.T) {
	_, err := Table().Build([]adapter.Spec{{ID: 1, Name: "chat", Kind: "bot", Credential: "x"}}, noHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")
}

func TestEveryKindCanCheckItsOptionsWithoutACredential(t *testing.T) {
	for kind, k := range Table() {
		assert.NotNil(t, k.Check, kind)
	}
	err := Table().Check([]adapter.Spec{{ID: 1, Name: "chat", Kind: "bot", Options: map[string]any{"endpoint": "https://api.example/bot{token}/{method}", "poll_timeout": "25s"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `channel "chat": option "poll_timeout" must be a whole number`)
}

// managedNode writes a node whose pao.yaml names a managed file.
func managedNode(t *testing.T, managed string) (pao, mf string) {
	t.Helper()
	dir := t.TempDir()
	pao, mf = filepath.Join(dir, "pao.yaml"), filepath.Join(dir, "managed.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(pao, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nmanaged: managed.yaml\n"+
		"channels:\n  - id: 1\n    name: mine\n    kind: bot\n    credential_env: PAO_BOT\n    options:\n      endpoint: \"https://bot.example/bot{token}/{method}\"\n"), 0o600))
	if managed != "" {
		require.NoError(t, os.WriteFile(mf, []byte(managed), 0o600))
	}
	return pao, mf
}

func TestAManagedChannelsAdapterErrorNamesTheManagedFile(t *testing.T) {
	pao, mf := managedNode(t, "channels:\n  - id: 2\n    name: ui-made\n    kind: bot\n    credential_env: PAO_UI\n    options:\n      nonsense: 1\n")
	cfg, err := config.Load(pao)
	require.NoError(t, err, "the options are the adapter's to judge, not the loader's")
	err = CheckChannels(pao, cfg.Orchestrator)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), mf+": "), err.Error())
	assert.Contains(t, err.Error(), `channel "ui-made"`)
}

func TestAWriteTheAdaptersWouldRefuseIsNotWritten(t *testing.T) {
	pao, mf := managedNode(t, "")
	check := func(cfg *config.Config) error { return CheckChannels(pao, cfg.Orchestrator) }
	bad := config.Managed{Channels: []config.Channel{{ID: 2, Name: "ui-made", Kind: "bot", CredentialEnv: "PAO_UI", Options: map[string]any{"nonsense": 1}}}}
	require.Error(t, config.WriteManaged(pao, bad, check))
	_, err := os.Stat(mf)
	assert.True(t, os.IsNotExist(err), "nothing was written")

	unknownKind := config.Managed{Channels: []config.Channel{{ID: 2, Name: "ui-made", Kind: "carrier-pigeon", CredentialEnv: "PAO_UI"}}}
	require.Error(t, config.WriteManaged(pao, unknownKind, check), "a kind this build does not have")

	good := config.Managed{Channels: []config.Channel{{ID: 2, Name: "ui-made", Kind: "bot", CredentialEnv: "PAO_UI", Options: map[string]any{"endpoint": "https://bot.example/bot{token}/{method}"}}}}
	require.NoError(t, config.WriteManaged(pao, good, check))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	require.NoError(t, CheckChannels(pao, cfg.Orchestrator), "what was written starts")
}
