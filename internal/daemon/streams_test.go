package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

type recChannel struct {
	name string
	mu   sync.Mutex
	got  []core.Message
}

func (c *recChannel) Name() string { return c.name }
func (c *recChannel) Send(_ context.Context, m core.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, m)
	return nil
}
func (c *recChannel) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, m := range c.got {
		out = append(out, m.Text)
	}
	return out
}

type streamRig struct {
	sc         *streamControl
	layer      *comm.Layer
	reg        *registry.Registry
	local      *recChannel
	other      *recChannel
	transcript string
}

func newStreamRig(t *testing.T) *streamRig {
	t.Helper()
	dir := t.TempDir()
	r := &streamRig{transcript: filepath.Join(dir, "bee.jsonl"), local: &recChannel{name: "local"}, other: &recChannel{name: "chat"}}
	require.NoError(t, os.WriteFile(r.transcript, nil, 0o600))
	r.reg = registry.New()
	require.NoError(t, r.reg.Add(core.Profile{Name: "bee", Transcript: core.Transcript{Format: "claude-code", Path: r.transcript}}))
	st, err := msgstore.Open(filepath.Join(dir, "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	r.layer = comm.New(st, route.New(r.reg, nil, nil))
	r.layer.AddChannel(r.local)
	r.layer.AddChannel(r.other)
	v := newViews()
	r.sc = &streamControl{layer: r.layer, reg: r.reg, views: v, level: 1,
		send: func(ctx context.Context, agent string, m core.Message) error {
			_, err := r.layer.Send(ctx, agent, m)
			return err
		}}
	return r
}

func (r *streamRig) say(t *testing.T, text string) {
	t.Helper()
	m := core.Message{Sender: "bee", Kind: core.MessageContent, Text: text}
	if r.sc.views.hold("bee", m) {
		return
	}
	_, err := r.layer.Send(context.Background(), "bee", m)
	require.NoError(t, err)
}

func (r *streamRig) write(t *testing.T, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(r.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	for _, l := range lines {
		_, err := f.WriteString(l + "\n")
		require.NoError(t, err)
	}
}

func TestSetStreamBindsTheOutputAndRemembersTheViewAndClearingForgetsIt(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, "alice"))
	ch, bound := r.layer.Bound("bee")
	assert.True(t, bound)
	assert.Equal(t, "chat", ch)
	level, viewer := r.sc.views.info("bee")
	assert.Equal(t, 3, level)
	assert.Equal(t, "alice", viewer)
	assert.Equal(t, 3, r.sc.views.level("bee", 1))
	assert.Equal(t, 1, r.sc.views.level("other", 1), "an agent with no view is rendered at the node's level")

	r.say(t, "hello")
	assert.Equal(t, []string{"hello"}, r.other.texts(), "only the channel it is bound to")
	assert.Empty(t, r.local.texts())

	require.NoError(t, r.sc.SetStream("bee", "", 0, ""))
	_, bound = r.layer.Bound("bee")
	assert.False(t, bound)
	level, viewer = r.sc.views.info("bee")
	assert.Zero(t, level)
	assert.Empty(t, viewer)
	r.say(t, "again")
	assert.Equal(t, []string{"again"}, r.local.texts(), "every channel again")
}

func TestSetStreamRefusesWhatItCannotDoAndChangesNothing(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 2, "alice"))

	assert.ErrorIs(t, r.sc.SetStream("nobody", "chat", 1, "x"), temp.ErrNotFound)
	assert.ErrorIs(t, r.sc.SetStream("bee", "no-such-channel", 1, "x"), temp.ErrBadRequest)
	assert.ErrorIs(t, r.sc.SetStream("bee", "chat", -1, "x"), temp.ErrBadRequest)

	ch, _ := r.layer.Bound("bee")
	assert.Equal(t, "chat", ch)
	level, viewer := r.sc.views.info("bee")
	assert.Equal(t, 2, level)
	assert.Equal(t, "alice", viewer, "a refused request leaves the stream as it was")
}

func TestAPausedViewHoldsOutputAndResumeSendsItInOrderBeforeLiveOutput(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 0, "alice"))
	require.NoError(t, r.sc.Pause("bee", "alice"))
	r.say(t, "one")
	r.say(t, "two")
	assert.Empty(t, r.other.texts(), "held while paused")

	// Output that arrives while the held part is going out goes after it.
	first := true
	r.sc.send = func(ctx context.Context, agent string, m core.Message) error {
		if first {
			first = false
			r.say(t, "three")
		}
		_, err := r.layer.Send(ctx, agent, m)
		return err
	}
	require.NoError(t, r.sc.Resume(context.Background(), "bee", "alice"))
	assert.Equal(t, []string{"one", "two", "three"}, r.other.texts())

	r.say(t, "four")
	assert.Equal(t, []string{"one", "two", "three", "four"}, r.other.texts(), "live again")
	require.NoError(t, r.sc.Resume(context.Background(), "bee", "alice"), "resuming a view that is not paused is not an error")
}

func TestAViewIsForItsViewerOnly(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 0, "alice"))
	for _, err := range []error{
		r.sc.Pause("bee", "bob"),
		r.sc.Resume(context.Background(), "bee", "bob"),
		r.sc.Backtrack(context.Background(), "bee", "bob", 3, 0),
	} {
		assert.ErrorIs(t, err, errViewer)
	}
	r.say(t, "x")
	assert.Equal(t, []string{"x"}, r.other.texts(), "bob's pause did not take")
	require.NoError(t, r.sc.Pause("bee", "alice"))

	assert.ErrorIs(t, r.sc.Pause("nobody", "alice"), temp.ErrNotFound)
}

func TestAPauseThatFillsDropsTheOldestAndTellsTheViewerOnResume(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 0, "alice"))
	require.NoError(t, r.sc.Pause("bee", "alice"))
	for i := 0; i < maxHeld+5; i++ {
		r.say(t, fmt.Sprintf("m%d", i))
	}
	require.NoError(t, r.sc.Resume(context.Background(), "bee", "alice"))
	got := r.other.texts()
	require.Len(t, got, maxHeld+1)
	assert.Contains(t, got[0], "5 earlier message(s) were not kept")
	assert.Equal(t, "m5", got[1], "the oldest five were let go")
	assert.Equal(t, fmt.Sprintf("m%d", maxHeld+4), got[len(got)-1])
}

func TestBacktrackShowsTheRecentPastAtTheLevelAskedForTheViewerAlone(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 0, "alice"))
	r.write(t,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"assistant","uuid":"a3","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private musing"}]}}`,
		`{"type":"assistant","uuid":"a4","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}`,
	)

	require.NoError(t, r.sc.Backtrack(context.Background(), "bee", "alice", 2, 0))
	got := r.other.texts()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "first answer")
	assert.Contains(t, got[0], "tool Bash")
	assert.Contains(t, got[0], "second answer")
	assert.NotContains(t, got[0], "private musing", "reasoning is level 3")
	assert.Empty(t, r.local.texts(), "to the channel the stream is bound to, not every channel")

	require.NoError(t, r.sc.Backtrack(context.Background(), "bee", "alice", 3, 2))
	last := r.other.texts()[1]
	assert.Contains(t, last, "the last 2 entr")
	assert.Contains(t, last, "private musing")
	assert.Contains(t, last, "second answer")
	assert.NotContains(t, last, "first answer", "only the last two")
	assert.Equal(t, core.MessageSystemEvent, r.other.got[1].Kind, "for the operator: no agent can quote or fetch it")
}

func TestBacktrackRefusesWhatItCannotShow(t *testing.T) {
	r := newStreamRig(t)
	ctx := context.Background()
	assert.ErrorIs(t, r.sc.Backtrack(ctx, "bee", "x", 0, 0), temp.ErrBadRequest, "a backtrack names a level")
	assert.ErrorIs(t, r.sc.Backtrack(ctx, "bee", "x", 2, maxBacktrack+1), temp.ErrBadRequest)
	assert.ErrorIs(t, r.sc.Backtrack(ctx, "bee", "x", 2, -1), temp.ErrBadRequest)
	assert.ErrorIs(t, r.sc.Backtrack(ctx, "nobody", "x", 2, 0), temp.ErrNotFound)

	require.NoError(t, os.Remove(r.transcript))
	assert.ErrorIs(t, r.sc.Backtrack(ctx, "bee", "x", 2, 0), temp.ErrNotFound, "no transcript to replay")

	require.NoError(t, os.WriteFile(r.transcript, nil, 0o600))
	require.NoError(t, r.sc.Backtrack(ctx, "bee", "x", 2, 0))
	assert.True(t, strings.Contains(strings.Join(r.local.texts(), ""), "nothing to show"), "an empty past is said so")
}

func TestWhatANodeReportsOfAStreamIsItsChannelLevelAndViewer(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, "alice"))
	src := registrySource{reg: r.reg, layer: r.layer, views: r.sc.views}
	snap := src.Snapshot()
	require.Len(t, snap, 1)
	require.Len(t, snap[0].GetBindings(), 1)
	b := snap[0].GetBindings()[0]
	assert.Equal(t, "chat", b.GetChannel())
	assert.Equal(t, int32(3), b.GetLevel())
	assert.Equal(t, "alice", b.GetViewer())
}

func TestLettingABindingGoDropsItsView(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, "alice"))
	require.NoError(t, r.sc.Pause("bee", "alice"))
	bindings{r.layer, r.sc.views}.Unbind("bee")
	level, viewer := r.sc.views.info("bee")
	assert.Zero(t, level)
	assert.Empty(t, viewer)
	r.say(t, "a name spawned again is not paused")
	assert.Len(t, r.local.texts(), 1)
}

func TestTheOperatorsStreamIsTheOperatorsViewAndAViewerMismatchIsAConflict(t *testing.T) {
	r := newStreamRig(t)
	api := streamAPI{streams: r.sc}
	require.NoError(t, api.SetStream(context.Background(), "bee", "chat", 2))
	_, viewer := r.sc.views.info("bee")
	assert.Equal(t, operatorViewer, viewer, "the view is the operator's, so no one else's control takes")

	err := translate(r.sc.Pause("bee", "someone-else"))
	require.Error(t, err)
	assert.ErrorIs(t, err, local.ErrConflict)
}

func TestAResumeThatFailsKeepsWhatItDidNotSendAndStaysPaused(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 0, "alice"))
	require.NoError(t, r.sc.Pause("bee", "alice"))
	for _, m := range []string{"one", "two", "three"} {
		r.say(t, m)
	}

	var sent []string
	r.sc.send = func(ctx context.Context, agent string, m core.Message) error {
		if m.Text == "two" {
			r.say(t, "four") // arrives while the batch is going out
			return fmt.Errorf("the channel is down")
		}
		sent = append(sent, m.Text)
		return nil
	}
	err := r.sc.Resume(context.Background(), "bee", "alice")
	require.Error(t, err)
	assert.Equal(t, []string{"one"}, sent)

	r.sc.send = func(ctx context.Context, agent string, m core.Message) error {
		sent = append(sent, m.Text)
		return nil
	}
	require.NoError(t, r.sc.Resume(context.Background(), "bee", "alice"), "still paused, so a retry works")
	assert.Equal(t, []string{"one", "two", "three", "four"}, sent, "nothing lost, in the agent's order")
}
