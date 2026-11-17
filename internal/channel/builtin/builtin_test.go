package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
)

type noHandler struct{}

func (noHandler) ReceiveFromChannel(context.Context, comm.ChannelInbound) (comm.ChannelResult, error) {
	return comm.ChannelResult{}, nil
}
func (noHandler) Recipients(string) []string             { return nil }
func (noHandler) NativeOf(string, string) (string, bool) { return "", false }

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
