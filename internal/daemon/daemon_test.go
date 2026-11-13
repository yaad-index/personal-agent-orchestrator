package daemon

import (
	"context"
	"encoding/json"
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
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

const token = "test-token"

// reportEnv is a startup value that makes the fake agent report its environment.
const reportEnv = "report-env"

// fakeAgent is a stand-in backend: a shell loop that, for every line pasted into
// its terminal, writes a human-side record and an assistant record to its
// transcript file, the way a real backend would.
const fakeAgent = `echo 'READY>'
[ -n "$REPORT_ENV" ] && printf '{"type":"assistant","uuid":"env","message":{"role":"assistant","content":[{"type":"text","text":"agent=%s gate=%s"}]}}\n' "$PAO_AGENT" "$PAO_GATE_SOCKET" >> "$TRANSCRIPT"
n=0
while IFS= read -r line; do
  n=$((n+1))
  printf '{"type":"user","uuid":"u%s","message":{"role":"user","content":"%s"}}\n' "$n" "$line" >> "$TRANSCRIPT"
  printf '{"type":"assistant","uuid":"a%s","message":{"role":"assistant","content":[{"type":"text","text":"echo: %s"}]}}\n' "$n" "$line" >> "$TRANSCRIPT"
done
`

type node struct {
	gateSock string
	stateDir string
	client   *local.Client
	cancel   context.CancelFunc
	done     chan error
	events   chan local.MessageJSON
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
		Gate: config.Gate{AutoAllow: []string{"read"}, AskTimeout: 5 * time.Second},
	}}
	for name, startup := range agents {
		file := filepath.Join(transcripts, name+".jsonl")
		cmd := startup
		switch startup {
		case "":
			cmd = fmt.Sprintf("env TRANSCRIPT=%s sh %s", file, scriptPath)
		case reportEnv:
			cmd = fmt.Sprintf("env REPORT_ENV=1 TRANSCRIPT=%s sh %s", file, scriptPath)
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
		gateSock: filepath.Join(cfg.Orchestrator.StateDir, "gate.sock"),
		stateDir: cfg.Orchestrator.StateDir,
		client:   &local.Client{URL: "http://" + ln.Addr().String(), Token: token},
		cancel:   cancel,
		done:     make(chan error, 1),
		events:   make(chan local.MessageJSON, 64),
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

func TestTheAgentIsStartedKnowingWhoItIsAndWhereTheGateIs(t *testing.T) {
	n := startNode(t, map[string]string{"a": reportEnv})

	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)

	// The fake agent reports its environment first, then echoes.
	got := n.next(t)
	assert.Equal(t, "agent=a gate="+n.gateSock, got.Text)
}

// askGate is what the backend's hook does: ask the gate about an action and
// block for the decision.
func askGate(t *testing.T, n *node, tool, input string) <-chan gate.Decision {
	t.Helper()
	out := make(chan gate.Decision, 1)
	go func() {
		d, _ := gate.Ask(context.Background(), n.gateSock, gate.Request{Agent: "a", Tool: tool, Input: json.RawMessage(input)})
		out <- d
	}()
	return out
}

// nextQuestion reads the stream until the gate's question arrives and returns
// its id.
func nextQuestion(t *testing.T, n *node) string {
	t.Helper()
	for {
		m := n.next(t)
		if m.Kind != "system_event" || m.Sender != "pao" {
			continue
		}
		require.Contains(t, m.Text, "/allow ")
		i := strings.Index(m.Text, "/allow ")
		return strings.Fields(m.Text[i+len("/allow "):])[0]
	}
}

func decisionWithin(t *testing.T, ch <-chan gate.Decision) gate.Decision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the gate did not decide")
		return gate.Decision{}
	}
}

func TestEndToEndGateAsksTheOperatorAndAbidesByTheAnswer(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	ctx := context.Background()

	// A read needs no question.
	d := decisionWithin(t, askGate(t, n, "Read", `{"file_path":"/x"}`))
	assert.True(t, d.Allow)

	// An action no rule covers is held until the operator answers: allow.
	res := askGate(t, n, "Bash", `{"command":"ls"}`)
	id := nextQuestion(t, n)
	select {
	case <-res:
		t.Fatal("the hook must stay blocked until the operator answers")
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, n.client.Decide(ctx, id, "allow", ""))
	d = decisionWithin(t, res)
	assert.True(t, d.Allow)
	assert.Equal(t, "operator", d.Via)

	// Deny, with a reason the agent is shown.
	res = askGate(t, n, "Bash", `{"command":"rm -rf /"}`)
	id = nextQuestion(t, n)
	require.NoError(t, n.client.Decide(ctx, id, "deny", "not on this box"))
	d = decisionWithin(t, res)
	assert.False(t, d.Allow)
	assert.Equal(t, "not on this box", d.Reason)

	// A late answer to a question already decided is refused.
	err := n.client.Decide(ctx, id, "allow", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestEndToEndAlwaysWritesTheApprovalBackIntoThePolicy(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	ctx := context.Background()

	res := askGate(t, n, "Bash", `{"command":"make test"}`)
	id := nextQuestion(t, n)
	require.NoError(t, n.client.Decide(ctx, id, "always", ""))
	assert.True(t, decisionWithin(t, res).Allow)

	// The same action is now covered without the operator.
	d := decisionWithin(t, askGate(t, n, "Bash", `{"command":"make test"}`))
	assert.True(t, d.Allow)
	assert.Equal(t, "rule", d.Via)

	// A different action still asks.
	res = askGate(t, n, "Bash", `{"command":"make test && rm -rf /"}`)
	id = nextQuestion(t, n)
	require.NoError(t, n.client.Decide(ctx, id, "deny", ""))
	assert.False(t, decisionWithin(t, res).Allow)

	b, err := os.ReadFile(filepath.Join(n.stateDir, "policy.json"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "make test")
	assert.Contains(t, string(b), `"source": "operator"`)
}

func TestRunRefusesAnUnknownTranscriptFormat(t *testing.T) {
	cfg := &config.Config{Profiles: []core.Profile{{Name: "a", Transcript: core.Transcript{Format: "nope"}}}}
	err := Run(context.Background(), cfg, token, Options{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unknown transcript format"))
}
