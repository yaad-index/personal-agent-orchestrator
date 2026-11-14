package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// say writes an assistant message of the agent's transcript, creating it if needed.
func say(t *testing.T, n *node, agent, text string) {
	t.Helper()
	line := fmt.Sprintf(`{"type":"assistant","uuid":"%s","timestamp":"2026-11-14T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, text, text)
	f, err := os.OpenFile(filepath.Join(n.dir, "transcripts", agent+".jsonl"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	require.NoError(t, err)
}

// sayTool writes a tool call, which is shown from level 2.
func sayTool(t *testing.T, n *node, agent, id, cmd string) {
	t.Helper()
	appendLine(t, n, agent, fmt.Sprintf(`{"type":"assistant","uuid":"%s","message":{"role":"assistant","content":[{"type":"tool_use","id":"%s","name":"Bash","input":{"command":%q}}]}}`, id, id, cmd))
}

// launch starts the agent by a message to it, which also starts following its
// transcript for the channel, and returns once its echo has arrived.
func launch(t *testing.T, from, on *node, agent string) {
	t.Helper()
	_, err := from.client.Post(context.Background(), "human", "hi", "name", agent)
	require.NoError(t, err)
	require.Equal(t, "echo: [human]: hi", on.next(t).Text)
}

// watching is a watch the operator has open through a node's local channel.
type watching struct {
	lines chan local.WatchLineJSON
	end   chan error
	stop  context.CancelFunc
}

func watch(t *testing.T, n *node, agent string, level, entries int) *watching {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	w := &watching{lines: make(chan local.WatchLineJSON, 64), end: make(chan error, 1), stop: stop}
	t.Cleanup(stop)
	go func() {
		w.end <- n.client.Watch(ctx, agent, level, entries, func(l local.WatchLineJSON) { w.lines <- l })
	}()
	return w
}

func (w *watching) next(t *testing.T) local.WatchLineJSON {
	t.Helper()
	select {
	case l := <-w.lines:
		return l
	case err := <-w.end:
		t.Fatalf("the watch ended: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no line in the watch")
	}
	return local.WatchLineJSON{}
}

func (w *watching) none(t *testing.T, d time.Duration, why string) {
	t.Helper()
	select {
	case l := <-w.lines:
		t.Fatalf("%s: got %q", why, l.Text)
	case <-time.After(d):
	}
}

// refused is the error of a watch that was not admitted.
func refused(t *testing.T, n *node, agent string, level, entries int) error {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	err := n.client.Watch(ctx, agent, level, entries, func(local.WatchLineJSON) {})
	require.Error(t, err, "the watch was admitted")
	return err
}

func TestAWatchShowsTheRecentPastAtTheLevelAskedThenWhatArrivesOnce(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	say(t, n, "bee", "one")
	sayTool(t, n, "bee", "t1", "ls")
	say(t, n, "bee", "two")
	say(t, n, "bee", "three")

	w := watch(t, n, "bee", 1, 2)
	l := w.next(t)
	assert.Equal(t, "two", l.Text, "the last 2 entries at level 1: the tool call is not one")
	assert.True(t, l.History)
	l = w.next(t)
	assert.Equal(t, "three", l.Text)
	assert.True(t, l.History)
	w.none(t, 300*time.Millisecond, "the past is shown once and then only what is new")

	say(t, n, "bee", "four")
	l = w.next(t)
	assert.Equal(t, "four", l.Text)
	assert.False(t, l.History, "after the watch began")
	sayTool(t, n, "bee", "t2", "pwd")
	w.none(t, 300*time.Millisecond, "a tool call is level 2, the watch is at level 1")

	w2 := watch(t, n, "bee", 2, 3)
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, w2.next(t).Text)
	}
	assert.Equal(t, []string{"three", "four", `tool Bash: {"command":"pwd"}`}, got, "at level 2 the tool call is in the last 3")
	sayTool(t, n, "bee", "t3", "date")
	assert.Equal(t, `tool Bash: {"command":"date"}`, w2.next(t).Text, "what arrives after is rendered at the level asked too")
}

func TestAWatchOfAnAgentWithNoTranscriptYetShowsItsFirstOutput(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	w := watch(t, n, "bee", 1, 0)
	w.none(t, 300*time.Millisecond, "nothing yet")
	say(t, n, "bee", "hello")
	l := w.next(t)
	assert.Equal(t, "hello", l.Text)
	assert.False(t, l.History)
}

func TestAWatchIsNotHeldBackByThePauseOfTheBoundView(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	ctx := context.Background()
	launch(t, n, n, "bee")
	require.NoError(t, n.client.SetStream(ctx, "bee", "local", 1))
	require.NoError(t, n.client.View(ctx, "bee", "pause", 0, 0))
	w := watch(t, n, "bee", 1, 0)
	require.Equal(t, "echo: [human]: hi", w.next(t).Text, "the past first")

	say(t, n, "bee", "while paused")
	assert.Equal(t, "while paused", w.next(t).Text, "the operator looking now is not held by a pause of the channel view")
	quiet(t, n, 300*time.Millisecond, "the bound view is still paused")
	require.NoError(t, n.client.View(ctx, "bee", "resume", 0, 0))
	assert.Equal(t, "while paused", n.next(t).Text, "and the pause still held it for the channel")
	w.none(t, 300*time.Millisecond, "resuming the channel shows the watch nothing twice")
}

func TestAWatchGoesToItsRequestAndIsKeptInNoScope(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	launch(t, n, n, "bee")
	w := watch(t, n, "bee", 1, 0)
	require.Equal(t, "echo: [human]: hi", w.next(t).Text, "the past first")
	say(t, n, "bee", "for the watcher")
	require.Equal(t, "for the watcher", w.next(t).Text)
	// The only thing the channel's own stream carries is what the follower sent for
	// the agent's binding; the watch added nothing to it.
	m := n.next(t)
	assert.Equal(t, "for the watcher", m.Text, "the agent's own output reaches the channel as before")
	quiet(t, n, 300*time.Millisecond, "one message for one line, however many are watching")
	assert.Empty(t, m.Reply)
}

func TestAWatchIsRefusedWithAReasonAndTakesNothing(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	for _, c := range []struct {
		name   string
		agent  string
		level  int
		entry  int
		status string
	}{
		{"no agent", "ghost", 1, 0, "404"},
		{"no level", "bee", 0, 0, "400"},
		{"too many entries", "bee", 1, maxBacktrack + 1, "400"},
	} {
		err := refused(t, n, c.agent, c.level, c.entry)
		assert.Contains(t, err.Error(), c.status, c.name)
	}
	// A refused watch holds no slot: the node still serves its whole bound.
	for i := 0; i < maxWatches; i++ {
		say(t, n, "bee", fmt.Sprintf("m%d", i))
		watch(t, n, "bee", 1, 1).next(t)
	}
}

func TestTheRouteNeedsTheToken(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	for _, auth := range []string{"", "Bearer wrong"} {
		req, err := http.NewRequest("GET", n.url+"/v1/watch/bee?level=1", nil)
		require.NoError(t, err)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "with %q", auth)
	}
}

func TestANodeServesAtMostItsBoundOfWatchesAndFreesASlotWhenOneEnds(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	say(t, n, "bee", "x")
	var open []*watching
	for i := 0; i < maxWatches; i++ {
		w := watch(t, n, "bee", 1, 1)
		w.next(t)
		open = append(open, w)
	}
	err := refused(t, n, "bee", 1, 1)
	assert.Contains(t, err.Error(), "429", "past the bound the answer is that the node is busy")
	assert.Contains(t, err.Error(), "as many watches as it allows")

	open[0].stop()
	require.Eventually(t, func() bool {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		got := make(chan struct{}, 1)
		go func() {
			_ = n.client.Watch(ctx, "bee", 1, 1, func(local.WatchLineJSON) {
				select {
				case got <- struct{}{}:
				default:
				}
			})
		}()
		select {
		case <-got:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}, 5*time.Second, 50*time.Millisecond, "a slot came back when a watch ended")
}

func TestTheMasterWatchesAnAgentOnAnotherNode(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	launch(t, a, b, "bee")
	say(t, b, "bee", "one")
	sayTool(t, b, "bee", "t1", "ls")
	say(t, b, "bee", "two")

	w := watch(t, a, "bee", 2, 2)
	l := w.next(t)
	assert.Equal(t, `tool Bash: {"command":"ls"}`, l.Text, "the master names the agent, the node renders at the level asked")
	assert.True(t, l.History)
	assert.Equal(t, "two", w.next(t).Text)
	w.none(t, 300*time.Millisecond, "the past once")

	say(t, b, "bee", "three")
	l = w.next(t)
	assert.Equal(t, "three", l.Text)
	assert.False(t, l.History)
	assert.Equal(t, "2026-11-14T10:00:00Z", l.Time, "the entry's own time travels")
	assert.Equal(t, "one", b.next(t).Text)
	assert.Equal(t, "two", b.next(t).Text)
	assert.Equal(t, "three", b.next(t).Text, "the agent's own node channel still gets its output")
	quiet(t, a, 200*time.Millisecond, "the master's local channel is not fed the stream: the watch is the operator's request")
}

func TestAWatchOnAnotherNodeIsNotHeldByThePauseThereAndHonoursItsRefusals(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	ctx := context.Background()
	launch(t, a, b, "bee")
	require.NoError(t, a.client.SetStream(ctx, "bee", "local", 1))
	require.NoError(t, a.client.View(ctx, "bee", "pause", 0, 0))

	w := watch(t, a, "bee", 1, 0)
	require.Equal(t, "echo: [human]: hi", w.next(t).Text, "the past first")
	say(t, b, "bee", "paused there")
	assert.Equal(t, "paused there", w.next(t).Text)
	quiet(t, b, 300*time.Millisecond, "held for the bound channel")

	assert.Contains(t, refused(t, a, "ghost", 1, 0).Error(), "404", "an agent nobody holds")
	assert.Contains(t, refused(t, a, "bee", 0, 0).Error(), "400", "the node's refusal keeps its meaning")
	assert.Contains(t, refused(t, a, "bee", 1, maxBacktrack+1).Error(), "400")
}

func TestTheMasterFillingANodesWatchesIsToldItIsBusy(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	say(t, b, "bee", "x")
	for i := 0; i < maxWatches; i++ {
		watch(t, a, "bee", 1, 1).next(t)
	}
	err := refused(t, a, "bee", 1, 1)
	assert.Contains(t, err.Error(), "429", "the node's bound reaches the operator as busy, not as a failure")
}

func TestAWatchOfAnAgentOnTheMasterItselfWorksTheSameWay(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", map[string]string{"queen": ""}, nil)
	f.startWith(t, "n-b", nil, nil)
	directoryHas(t, a, "n-b", 0)
	say(t, a, "queen", "mine")
	assert.Equal(t, "mine", watch(t, a, "queen", 1, 0).next(t).Text)
}

func TestAWatchEndsWhenTheOperatorStopsItAndTheNodeFreesIt(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	say(t, b, "bee", "x")
	for round := 0; round < maxWatches+2; round++ {
		w := watch(t, a, "bee", 1, 1)
		w.next(t)
		w.stop()
		select {
		case err := <-w.end:
			assert.NoError(t, err, "a stop by the operator is not an error")
		case <-time.After(5 * time.Second):
			t.Fatal("the watch did not end")
		}
		// The slot on the node comes back, or a later round would be refused.
	}
}

func TestAWatchCannotBeUsedToTellWhichAgentsExist(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Add(core.Profile{Name: "bee", Tags: []string{"t"}, Transcript: core.Transcript{Format: claudecode.Format, Path: "/nonexistent"}}))
	w := newWatcher(reg, time.Millisecond)

	// A watch that was wrongly admitted would follow the transcript until its context
	// ends, so the context is short and the answer is what is asserted.
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	none := func() {}
	ghost := w.watch(ctx, access.Operator, "ghost", 1, 0, none, func(watchLine) error { return nil })
	denied := w.watch(ctx, access.Agent("someone"), "bee", 1, 0, none, func(watchLine) error { return nil })
	require.ErrorIs(t, ghost, temp.ErrNotFound)
	require.ErrorIs(t, denied, temp.ErrNotFound)
	assert.Equal(t, strings.ReplaceAll(ghost.Error(), "ghost", "bee"), denied.Error(),
		"an agent the caller may not watch is answered in the words of one that is absent")
	assert.Equal(t, codes.NotFound, status.Code(grpcErr(denied)))
}
