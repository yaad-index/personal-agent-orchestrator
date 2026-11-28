package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// reportToken is a startup value that makes the fake agent report the value of
// the environment variable the node's token came from.
const reportToken = "report-token"

// oldSession is a startup value for an agent whose transcript path is a
// directory that already holds an earlier session's transcript. The agent
// reports its environment first, into a new file, like a fresh session.
const oldSession = "old-session"

// fakeAgent is a stand-in backend: a shell loop that, for every line pasted into
// its terminal, writes a human-side record and an assistant record to its
// transcript file, the way a real backend would.
const fakeAgent = `printf '%s\n' "$@" > "$TRANSCRIPT.args"
env | grep '^CLAUDE_CODE_SYNC_' | sort > "$TRANSCRIPT.env"
printf '%s' "$PAO_AGENT_TOKEN" > "$TRANSCRIPT.token"
echo 'READY>'
[ -n "$REPORT_ENV" ] && printf '{"type":"assistant","uuid":"env","message":{"role":"assistant","content":[{"type":"text","text":"agent=%s gate=%s"}]}}\n' "$PAO_AGENT" "$PAO_GATE_SOCKET" >> "$TRANSCRIPT"
[ -n "$REPORT_TOKEN" ] && printf '{"type":"assistant","uuid":"tok","message":{"role":"assistant","content":[{"type":"text","text":"token=[%s]"}]}}\n' "$PAO_TEST_NODE_TOKEN" >> "$TRANSCRIPT"
n=0
while IFS= read -r line; do
  n=$((n+1))
  printf '{"type":"user","uuid":"u%s","message":{"role":"user","content":"%s"}}\n' "$n" "$line" >> "$TRANSCRIPT"
  printf '{"type":"assistant","uuid":"a%s","message":{"role":"assistant","content":[{"type":"text","text":"echo: %s"}]}}\n' "$n" "$line" >> "$TRANSCRIPT"
done
`

type node struct {
	dir        string
	sock       string
	scriptPath string
	gateSock   string
	stateDir   string
	url        string
	client     *local.Client
	cancel     context.CancelFunc
	done       chan error
	events     chan local.MessageJSON

	// askErrs are the errors of gate questions that could not be asked, kept so
	// a test waiting for a question that never came can say why.
	askMu   sync.Mutex
	askErrs []error

	// What a run of the daemon needs, kept so the node can be restarted.
	cfg      *config.Config
	token    string
	optFns   []func(*Options)
	streamWG sync.WaitGroup
}

// startNode runs a daemon over real tmux with one agent per name.
func startNode(t *testing.T, agents map[string]string) *node {
	t.Helper()
	return startNodeWith(t, agents, nil)
}

// startNodeWith is startNode with a hook to adjust the config before the node
// starts. The node's directory is created first, so the hook can use it.
func startNodeWith(t *testing.T, agents map[string]string, adjust func(dir, script string, cfg *config.Config), optFns ...func(*Options)) *node {
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
		tpath := file
		cmd := startup
		switch startup {
		case oldSession:
			tdir := filepath.Join(transcripts, name+"-dir")
			require.NoError(t, os.MkdirAll(tdir, 0o755))
			old := filepath.Join(tdir, "earlier-session.jsonl")
			require.NoError(t, os.WriteFile(old, []byte(`{"type":"assistant","uuid":"old","message":{"role":"assistant","content":[{"type":"text","text":"STALE earlier session"}]}}`+"\n"), 0o600))
			require.NoError(t, os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))
			file = filepath.Join(tdir, "new-session.jsonl")
			tpath = tdir
			cmd = fmt.Sprintf("env REPORT_ENV=1 TRANSCRIPT=%s sh %s", file, scriptPath)
		case "":
			cmd = fmt.Sprintf("env TRANSCRIPT=%s sh %s", file, scriptPath)
		case reportToken:
			cmd = fmt.Sprintf("env REPORT_TOKEN=1 TRANSCRIPT=%s sh %s", file, scriptPath)
		case reportEnv:
			cmd = fmt.Sprintf("env REPORT_ENV=1 TRANSCRIPT=%s sh %s", file, scriptPath)
		}
		cfg.Profiles = append(cfg.Profiles, core.Profile{
			Name: name, Tags: []string{"team"}, Backend: "claude-code", Workdir: dir, User: "alice",
			Startup:    core.Startup{Command: cmd, ReadyPattern: `READY>`, ReadyTimeout: 700 * time.Millisecond},
			Transcript: core.Transcript{Format: "claude-code", Path: tpath},
		})
	}

	// The operator reaches the test agents (tag team) and the agents spawned from
	// the test recipes (tag temp) through grants, as an operator does once it has
	// granted itself in. A hook may write its own grants over these.
	require.NoError(t, os.MkdirAll(cfg.Orchestrator.StateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, grantsFile), []byte(operatorTestGrants), 0o600))
	// And it has once answered "always" to an agent's message to another: pao's
	// sending is judged on pao's side, and the tests about something else should
	// not wait on that question. A hook may write its own policy over this.
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, "policy.json"), []byte(`[{"tool":"pao.route","effect":"allow"}]`), 0o600))

	if adjust != nil {
		adjust(dir, scriptPath, cfg)
	}

	n := &node{
		dir: dir, sock: sock, scriptPath: scriptPath,
		gateSock: filepath.Join(cfg.Orchestrator.StateDir, "gate.sock"),
		stateDir: cfg.Orchestrator.StateDir,
		cfg:      cfg, token: token, optFns: optFns,
	}
	n.run(t)
	// One cleanup, in the order that works: stop the daemon first (which also
	// ends the stream's context), then wait for the stream reader. It stops
	// whichever run is current, so a restarted node is stopped too.
	t.Cleanup(func() { n.stop(t) })
	return n
}

// run starts the daemon on the node's config and waits until its stream and
// gate are open.
func (n *node) run(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	n.url = "http://" + ln.Addr().String()
	n.client = &local.Client{URL: n.url, Token: n.token}
	n.cancel = cancel
	n.done = make(chan error, 1)
	n.events = make(chan local.MessageJSON, 64)
	done, events, client := n.done, n.events, n.client
	go func() {
		o := Options{Listener: ln, MirrorPoll: 10 * time.Millisecond, PollInterval: 20 * time.Millisecond}
		for _, f := range n.optFns {
			f(&o)
		}
		done <- Run(ctx, n.cfg, n.token, o)
	}()

	// Stream from the start: the test goes on once the server has taken the
	// stream, so nothing sent from here on is missed.
	n.streamWG.Add(1)
	opened := make(chan struct{})
	streamErr := make(chan error, 1)
	go func() {
		defer n.streamWG.Done()
		streamErr <- client.StreamOpened(ctx, func() { close(opened) }, func(m local.MessageJSON) { events <- m })
	}()
	select {
	case <-opened:
	case err := <-streamErr:
		t.Fatalf("the stream did not open: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not open")
	}
	// The gate's socket is made a little after the server starts; a hook that
	// asks before it exists is denied.
	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", n.gateSock)
		if err == nil {
			_ = conn.Close()
		}
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the gate's socket did not open")
}

// stop ends the current run of the daemon, leaving the multiplexer's sessions
// as they are, as a pao that stops does (ADR-0051 point 1).
func (n *node) stop(t *testing.T) {
	t.Helper()
	if n.cancel == nil {
		return
	}
	n.cancel()
	n.cancel = nil
	select {
	case err := <-n.done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Error("daemon did not stop")
	}
	n.streamWG.Wait()
}

// restart stops the daemon and starts it again on the same config and state.
func (n *node) restart(t *testing.T) {
	t.Helper()
	n.stop(t)
	n.run(t)
}

const operatorTestGrants = `{"version":1,"grants":[{"subject":"operator","kind":"agent","tag":"team"},{"subject":"operator","kind":"agent","tag":"temp"}]}`

func (n *node) next(t *testing.T) local.MessageJSON {
	t.Helper()
	select {
	case m := <-n.events:
		return m
	case <-time.After(10 * time.Second):
		n.askMu.Lock()
		defer n.askMu.Unlock()
		t.Fatalf("no message on the stream (gate asks that failed: %v)", n.askErrs)
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

func TestAFreshSessionsFirstLinesArriveWholeAndAnOldSessionIsNotReplayed(t *testing.T) {
	n := startNode(t, map[string]string{"a": oldSession})

	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)

	// The agent wrote its environment line while Launch was still polling for
	// readiness, into a file that did not exist when following began: it must
	// arrive whole. The earlier session's content, already in the directory
	// when following began, must not.
	first := n.next(t)
	assert.Equal(t, "agent=a gate="+n.gateSock, first.Text)
	second := n.next(t)
	assert.Equal(t, "echo: [human]: hi", second.Text)

	select {
	case m := <-n.events:
		t.Fatalf("unexpected message, an earlier session must not be replayed: %+v", m)
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

func TestTheBackendAdapterStartsTheAgentWithOnlyPaosContext(t *testing.T) {
	var transcripts string
	n := startNodeWith(t, map[string]string{"a": ""}, func(dir, _ string, _ *config.Config) {
		transcripts = filepath.Join(dir, "transcripts")
	})
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)
	n.next(t)

	b, err := os.ReadFile(filepath.Join(transcripts, "a.jsonl.args"))
	require.NoError(t, err)
	args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for _, opt := range []string{"--strict-mcp-config", "--disable-slash-commands"} {
		assert.Contains(t, args, opt)
	}
	value := func(opt string) string {
		i := slices.Index(args, opt)
		require.GreaterOrEqual(t, i, 0, "no %s", opt)
		require.Less(t, i+1, len(args), "%s has no value", opt)
		return args[i+1]
	}
	assert.Equal(t, "project,local", value("--setting-sources"))
	assert.Contains(t, value("--mcp-config"), `"pao":`, "pao's MCP server, under the name the gate's rules use")

	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
		Excludes   []string `json:"claudeMdExcludes"`
		AutoMemory *bool    `json:"autoMemoryEnabled"`
	}
	require.NoError(t, json.Unmarshal([]byte(value("--settings")), &settings))
	require.Len(t, settings.Hooks.PreToolUse, 1)
	require.Len(t, settings.Hooks.PreToolUse[0].Hooks, 1)
	h := settings.Hooks.PreToolUse[0].Hooks[0]
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, "'"+exe+"' hook", h.Command, "this program is the gate hook")
	assert.Equal(t, 65, h.Timeout, "the gate's ask timeout (5s) and the margin")
	assert.NotEmpty(t, settings.Excludes)
	require.NotNil(t, settings.AutoMemory)
	assert.False(t, *settings.AutoMemory)

	env, err := os.ReadFile(filepath.Join(transcripts, "a.jsonl.env"))
	require.NoError(t, err)
	assert.Equal(t, "CLAUDE_CODE_SYNC_PLUGINS=0\nCLAUDE_CODE_SYNC_SKILLS=0\n", string(env), "the adapter's environment reaches the agent")
}

// askGate is what the backend's hook does: ask the gate about an action and
// block for the decision.
func askGate(t *testing.T, n *node, tool, input string) <-chan gate.Decision {
	t.Helper()
	out := make(chan gate.Decision, 1)
	go func() {
		d, err := gate.Ask(context.Background(), n.gateSock, gate.Request{Agent: "a", Tool: tool, Input: json.RawMessage(input)})
		if err != nil {
			n.askMu.Lock()
			n.askErrs = append(n.askErrs, fmt.Errorf("asking the gate about %s: %w", tool, err))
			n.askMu.Unlock()
		}
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
		require.Contains(t, m.Text, "/pao allow ")
		i := strings.Index(m.Text, "/pao allow ")
		return strings.Fields(m.Text[i+len("/pao allow "):])[0]
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
	assert.True(t, d.Allow, d.Reason)

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

func TestAgentsDoNotInheritTheOperatorsToken(t *testing.T) {
	t.Setenv("PAO_TEST_NODE_TOKEN", "operator-secret")
	n := startNodeWith(t, map[string]string{"a": reportToken}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.TokenEnv = "PAO_TEST_NODE_TOKEN"
	})

	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)

	got := n.next(t)
	assert.Equal(t, "token=[]", got.Text, "the variable the node's token came from is not in the agent's environment")
	assert.NotContains(t, got.Text, "operator-secret")
}

func TestAnAgentGetsItsTokenWithoutItOnAnyCommandLine(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)
	n.next(t)

	file := filepath.Join(n.stateDir, "agent-tokens", "a")
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "readable by the node user alone")
	dirInfo, err := os.Stat(filepath.Dir(file))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	want, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Regexp(t, `^[0-9a-f]{64}$`, string(want))
	got, err := os.ReadFile(filepath.Join(n.dir, "transcripts", "a.jsonl.token"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the agent starts with its token in its environment")

	procs, err := filepath.Glob("/proc/[0-9]*/cmdline")
	require.NoError(t, err)
	require.NotEmpty(t, procs)
	for _, p := range procs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue // gone, or another user's
		}
		assert.NotContains(t, string(b), string(want), "%s carries the token on its command line", p)
	}
	out, err := exec.Command("tmux", "-L", n.sock, "show-environment", "-t", "=pao-a").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NotContains(t, string(out), string(want), "nor the session's environment, which the multiplexer was given on its command line")
}

func TestAStoppedAgentsTokenIsGone(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	_, err := n.client.Post(context.Background(), "human", "hi", "name", "a")
	require.NoError(t, err)
	n.next(t)
	file := filepath.Join(n.stateDir, "agent-tokens", "a")
	require.FileExists(t, file)
	require.NoError(t, n.client.AgentAction(context.Background(), "a", "stop"))
	assert.NoFileExists(t, file)
}
