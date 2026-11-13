package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

const token = "test-token"

// fakeAgent is a stand-in backend: a shell loop that, for every line pasted into
// its terminal, writes a human-side record and an assistant record to its
// transcript file, the way a real backend would.
const fakeAgent = `echo 'READY>'
n=0
while IFS= read -r line; do
  n=$((n+1))
  printf '{"type":"user","uuid":"u%s","message":{"role":"user","content":"%s"}}\n' "$n" "$line" >> "$TRANSCRIPT"
  printf '{"type":"assistant","uuid":"a%s","message":{"role":"assistant","content":[{"type":"text","text":"echo: %s"}]}}\n' "$n" "$line" >> "$TRANSCRIPT"
done
`

type node struct {
	client *local.Client
	cancel context.CancelFunc
	done   chan error
	events chan local.MessageJSON
}

// startNode runs a daemon over real tmux with one agent per name.
func startNode(t *testing.T, agents map[string]string) *node {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "agent.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(fakeAgent), 0o700))
	transcripts := filepath.Join(dir, "transcripts")
	require.NoError(t, os.MkdirAll(transcripts, 0o755))

	sock := fmt.Sprintf("pao-daemon-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", sock, "kill-server").Run() })

	cfg := &config.Config{Orchestrator: config.Orchestrator{
		Node: "n1", StateDir: filepath.Join(dir, "state"), TmuxSocket: sock,
		MessageWindow: 100, Level: 1,
	}}
	for name, startup := range agents {
		file := filepath.Join(transcripts, name+".jsonl")
		cmd := startup
		if cmd == "" {
			cmd = fmt.Sprintf("env TRANSCRIPT=%s sh %s", file, scriptPath)
		}
		cfg.Profiles = append(cfg.Profiles, core.Profile{
			Name: name, Tags: []string{"team"}, Backend: "fake", Workdir: dir,
			Startup:    core.Startup{Command: cmd, ReadyPattern: `READY>`, ReadyTimeout: 700 * time.Millisecond},
			Transcript: core.Transcript{Format: "claude-code", Path: file},
		})
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	n := &node{
		client: &local.Client{URL: "http://" + ln.Addr().String(), Token: token},
		cancel: cancel,
		done:   make(chan error, 1),
		events: make(chan local.MessageJSON, 64),
	}
	go func() {
		n.done <- Run(ctx, cfg, token, Options{Listener: ln, MirrorPoll: 10 * time.Millisecond, PollInterval: 20 * time.Millisecond})
	}()

	// Stream from the start, once the server is up.
	require.Eventually(t, func() bool {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			_ = conn.Close()
		}
		return err == nil
	}, 3*time.Second, 10*time.Millisecond)
	var wg sync.WaitGroup
	wg.Add(1)
	ready := make(chan struct{})
	go func() {
		defer wg.Done()
		close(ready)
		_ = n.client.Stream(ctx, func(m local.MessageJSON) { n.events <- m })
	}()
	<-ready
	time.Sleep(100 * time.Millisecond) // let the stream attach

	// One cleanup, in the order that works: stop the daemon first (which also
	// ends the stream's context), then wait for the stream reader.
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-n.done:
			assert.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
		wg.Wait()
	})
	return n
}

func (n *node) next(t *testing.T) local.MessageJSON {
	t.Helper()
	select {
	case m := <-n.events:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no message on the stream")
		return local.MessageJSON{}
	}
}

func TestEndToEndHumanToAgentAndBack(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	ctx := context.Background()

	rep, err := n.client.Post(ctx, "human", "hello there", "name", "a")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.True(t, rep.Results[0].Launched, "the first input starts the agent")
	assert.Empty(t, rep.Results[0].Error)

	got := n.next(t)
	assert.Equal(t, "a", got.Sender)
	assert.Equal(t, "echo: [human]: hello there", got.Text, "the agent read the one sender-tagged line and answered")
	assert.Equal(t, "content", got.Kind)

	rep, err = n.client.Post(ctx, "human", "second", "name", "a")
	require.NoError(t, err)
	assert.False(t, rep.Results[0].Launched, "the agent is already up")
	got = n.next(t)
	assert.Equal(t, "echo: [human]: second", got.Text)

	select {
	case extra := <-n.events:
		t.Fatalf("unexpected extra message (the human side of the transcript must not be echoed): %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEndToEndTagFansOutToEveryAgent(t *testing.T) {
	n := startNode(t, map[string]string{"a": "", "b": ""})

	rep, err := n.client.Post(context.Background(), "human", "all hands", "tag", "team")
	require.NoError(t, err)
	require.Len(t, rep.Results, 2)

	senders := map[string]string{}
	for range 2 {
		m := n.next(t)
		senders[m.Sender] = m.Text
	}
	assert.Equal(t, map[string]string{
		"a": "echo: [human]: all hands",
		"b": "echo: [human]: all hands",
	}, senders)
}

func TestEndToEndUnknownTarget(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})

	_, err := n.client.Post(context.Background(), "human", "x", "name", "nobody")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestEndToEndReadinessTimeoutShowsTheOperatorThePane(t *testing.T) {
	n := startNode(t, map[string]string{"stuck": "sh -c \"echo please log in to continue; exec cat\""})

	rep, err := n.client.Post(context.Background(), "human", "hi", "name", "stuck")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Contains(t, rep.Results[0].Error, "not ready")
	assert.NotContains(t, rep.Results[0].Error, "please log in", "the pane is not in the routed result")

	got := n.next(t)
	assert.Equal(t, "system_event", got.Kind)
	assert.Equal(t, "pao", got.Sender)
	assert.Contains(t, got.Text, "please log in to continue", "the operator sees the raw pane")
}

func TestRunRefusesAnUnknownTranscriptFormat(t *testing.T) {
	cfg := &config.Config{Profiles: []core.Profile{{Name: "a", Transcript: core.Transcript{Format: "nope"}}}}
	err := Run(context.Background(), cfg, token, Options{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unknown transcript format"))
}
