package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// botFake is an adapter that does nothing but expose the handler it was given.
type botFake struct {
	spec    adapter.Spec
	h       adapter.Handler
	started chan struct{}
	stopped chan struct{}
	mu      sync.Mutex
	sent    []core.Message
}

func (b *botFake) Name() string { return b.spec.Name }
func (b *botFake) Send(_ context.Context, m core.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, m)
	return nil
}
func (b *botFake) Run(ctx context.Context) error {
	close(b.started)
	<-ctx.Done()
	time.Sleep(200 * time.Millisecond) // a slow stop, so a node that does not wait is caught
	close(b.stopped)
	return nil
}

// fakeTable builds botFakes and hands each over a channel: the factory runs on the
// node's own goroutine, so the test must not read what it made except by receiving
// it here.
func fakeTable(built chan<- *botFake) adapter.Table {
	return adapter.Table{"bot": func(s adapter.Spec, h adapter.Handler) (adapter.Adapter, error) {
		b := &botFake{spec: s, h: h, started: make(chan struct{}), stopped: make(chan struct{})}
		built <- b
		return b, nil
	}}
}

func theBot(t *testing.T, built <-chan *botFake) *botFake {
	t.Helper()
	select {
	case b := <-built:
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("no adapter was built")
		return nil
	}
}

func withChannel(cfg *config.Config) {
	cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_CRED"}}
}

func TestAConfiguredChannelIsBuiltRunStoppedAndItsCredentialIsKeptFromAgents(t *testing.T) {
	t.Setenv("PAO_TEST_BOT_CRED", "s3cret")
	built := make(chan *botFake, 1)
	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) { withChannel(cfg) },
		func(o *Options) { o.Adapters = fakeTable(built) })
	bot := theBot(t, built)
	select {
	case <-bot.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the adapter was never run")
	}
	assert.Equal(t, adapter.Spec{ID: 1, Name: "chat", Kind: "bot", Credential: "s3cret"}, bot.spec)
	assert.Empty(t, os.Getenv("PAO_TEST_BOT_CRED"), "taken out of the environment, so an agent cannot read it")

	ledger, err := os.ReadFile(filepath.Join(n.stateDir, "channels.json"))
	require.NoError(t, err)
	assert.Contains(t, string(ledger), `"1": "chat"`)

	n.cancel()
	select {
	case err := <-n.done:
		n.done <- err // the harness's cleanup reads it too
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not stop")
	}
	select {
	case <-bot.stopped:
	default:
		t.Fatal("the node returned before its adapter had stopped")
	}
}

func TestInboundThroughAChannelIsRecordedRoutedAndAttributedBySubject(t *testing.T) {
	t.Setenv("PAO_TEST_BOT_CRED", "x")
	built := make(chan *botFake, 1)
	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) { withChannel(cfg) },
		func(o *Options) { o.Adapters = fakeTable(built) })
	bot := theBot(t, built)
	ctx := context.Background()
	<-bot.started

	res, err := bot.h.ReceiveFromChannel(ctx, comm.ChannelInbound{Channel: "chat", Sender: "900", Text: "hello", Target: core.Target{Kind: core.ByName, Value: "bee"}, Raw: "RAW-1"})
	require.NoError(t, err)
	assert.Equal(t, comm.ChannelResult{}, res, "a stranger is told nothing")
	rej, err := n.client.Rejected(ctx)
	require.NoError(t, err)
	require.Len(t, rej, 1)
	assert.Equal(t, local.RejectedJSON{Channel: "chat", Sender: "900", Kind: "message", Reason: "no subject", First: rej[0].First, Last: rej[0].Last, Count: 1, Payload: "RAW-1"}, rej[0])

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	res, err = bot.h.ReceiveFromChannel(ctx, comm.ChannelInbound{Channel: "chat", Sender: "900", Registration: true, Text: inv.Secret})
	require.NoError(t, err)
	assert.Equal(t, comm.ReplyRegistered, res.Reply)

	res, err = bot.h.ReceiveFromChannel(ctx, comm.ChannelInbound{Channel: "chat", Sender: "900", Text: "hello", Target: core.Target{Kind: core.ByName, Value: "bee"}})
	require.NoError(t, err)
	assert.Equal(t, comm.ReplyNoAccess, res.Reply, "registered, but not flagged operator")
	assert.Nil(t, res.Delivery)

	require.NoError(t, n.client.SetOperator(ctx, "alice", true))
	res, err = bot.h.ReceiveFromChannel(ctx, comm.ChannelInbound{Channel: "chat", Sender: "900", Text: "hello", Target: core.Target{Kind: core.ByName, Value: "bee"}, Native: "55"})
	require.NoError(t, err)
	require.NotNil(t, res.Delivery)
	assert.Equal(t, "alice", res.Delivery.Message.Sender, "attributed under the subject, not the external id")
	native, ok := bot.h.NativeOf("chat", "1-55")
	assert.True(t, ok)
	assert.Equal(t, "55", native)
	_, ok = bot.h.NativeOf("chat", "2-55")
	assert.False(t, ok)
}

func TestAChannelOfAKindNotCompiledInStopsTheNode(t *testing.T) {
	t.Setenv("PAO_TEST_BOT_CRED", "x")
	_, err := wireChannelsForTest(t, config.Channel{ID: 1, Name: "chat", Kind: "nope", CredentialEnv: "PAO_TEST_BOT_CRED"}, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not compiled into this build")
}

func TestAnUnsetCredentialStopsTheNode(t *testing.T) {
	_, err := wireChannelsForTest(t, config.Channel{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_CRED"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential")
}

func TestAReusedChannelIdStopsTheNodeBeforeAnythingStarts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channels.json"), []byte(`{"channels":{"1":"oldchat"}}`), 0o600))
	cfg := &config.Config{Orchestrator: config.Orchestrator{StateDir: dir, MessageWindow: 10,
		Channels: []config.Channel{{ID: 1, Name: "newchat", Kind: "bot", CredentialEnv: "E"}}}}
	built := make(chan *botFake, 1)
	_, err := wireChannels(cfg, map[string]string{"newchat": "x"}, fakeTable(built), comm.New(nil, nil), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never reused")
	assert.Empty(t, built, "no adapter was built")
}

func TestAConfiguredChannelBecomesAChannelOfTheLayer(t *testing.T) {
	built := make(chan *botFake, 1)
	cfg := &config.Config{Orchestrator: config.Orchestrator{StateDir: t.TempDir(), MessageWindow: 10,
		Channels: []config.Channel{{ID: 4, Name: "chat", Kind: "bot", CredentialEnv: "E"}}}}
	l := comm.New(nil, nil)
	as, err := wireChannels(cfg, map[string]string{"chat": "x"}, fakeTable(built), l, nil, nil)
	require.NoError(t, err)
	require.Len(t, as, 1)
	assert.True(t, l.HasChannel("chat"))
	native, ok := l.NativeOf("chat", "4-9")
	assert.True(t, ok, "the layer knows the channel's id")
	assert.Equal(t, "9", native)
}

func wireChannelsForTest(t *testing.T, c config.Channel, cred string) ([]adapter.Adapter, error) {
	t.Helper()
	built := make(chan *botFake, 1)
	cfg := &config.Config{Orchestrator: config.Orchestrator{StateDir: t.TempDir(), MessageWindow: 10, Channels: []config.Channel{c}}}
	l := comm.New(nil, nil)
	return wireChannels(cfg, map[string]string{c.Name: cred}, fakeTable(built), l, nil, nil)
}
