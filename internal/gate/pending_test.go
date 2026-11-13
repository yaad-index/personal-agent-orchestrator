package gate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notes struct {
	mu   sync.Mutex
	got  []string
	err  error
	sent chan struct{}
}

func newNotes() *notes { return &notes{sent: make(chan struct{}, 8)} }

func (n *notes) notify(_ context.Context, text string) error {
	n.mu.Lock()
	n.got = append(n.got, text)
	err := n.err
	n.mu.Unlock()
	n.sent <- struct{}{}
	return err
}

func (n *notes) last() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.got[len(n.got)-1]
}

func ask(p *Pending, ctx context.Context, agent, tool, input string) <-chan Answer {
	out := make(chan Answer, 1)
	go func() {
		a, err := p.Ask(ctx, Request{Agent: agent, Tool: tool}, input)
		if err != nil {
			a = Answer{Reason: "ERR:" + err.Error()}
		}
		out <- a
	}()
	return out
}

func waitSent(t *testing.T, n *notes) {
	t.Helper()
	select {
	case <-n.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
}

func idIn(t *testing.T, text string) string {
	t.Helper()
	i := strings.Index(text, "/allow ")
	require.GreaterOrEqual(t, i, 0, text)
	return strings.Fields(text[i+len("/allow "):])[0]
}

func TestAskAnnouncesAndWaitsForTheAnswer(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	res := ask(p, context.Background(), "a", "Bash", `{"command":"ls"}`)
	waitSent(t, n)

	text := n.last()
	assert.Contains(t, text, `agent "a"`)
	assert.Contains(t, text, "Bash")
	assert.Contains(t, text, `{"command":"ls"}`)
	assert.Equal(t, 1, p.Waiting())

	require.NoError(t, p.Answer(idIn(t, text), Answer{Allow: true, Remember: true}))
	select {
	case a := <-res:
		assert.Equal(t, Answer{Allow: true, Remember: true}, a)
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return")
	}
	assert.Zero(t, p.Waiting())
}

func TestEachQuestionHasItsOwnId(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	r1 := ask(p, context.Background(), "a", "Bash", `{"c":1}`)
	waitSent(t, n)
	id1 := idIn(t, n.last())
	r2 := ask(p, context.Background(), "b", "Edit", `{"c":2}`)
	waitSent(t, n)
	id2 := idIn(t, n.last())
	require.NotEqual(t, id1, id2)

	require.NoError(t, p.Answer(id2, Answer{Allow: false, Reason: "no"}))
	assert.Equal(t, "no", (<-r2).Reason)
	select {
	case <-r1:
		t.Fatal("answering one question must not answer the other")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, p.Answer(id1, Answer{Allow: true}))
	assert.True(t, (<-r1).Allow)
}

func TestAnAnswerIsTakenOnce(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	res := ask(p, context.Background(), "a", "Bash", `{}`)
	waitSent(t, n)
	id := idIn(t, n.last())

	require.NoError(t, p.Answer(id, Answer{Allow: true}))
	<-res
	err := p.Answer(id, Answer{Allow: false})
	assert.ErrorIs(t, err, ErrNoSuchQuestion, "a second answer cannot flip a decision already made")
}

func TestUnknownQuestion(t *testing.T) {
	p := NewPending(newNotes().notify)
	assert.ErrorIs(t, p.Answer("q404", Answer{Allow: true}), ErrNoSuchQuestion)
}

func TestACancelledQuestionStopsWaitingAndCannotBeAnswered(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	ctx, cancel := context.WithCancel(context.Background())
	res := ask(p, ctx, "a", "Bash", `{}`)
	waitSent(t, n)
	id := idIn(t, n.last())

	cancel()
	select {
	case a := <-res:
		assert.Contains(t, a.Reason, "ERR:")
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after cancel")
	}
	assert.ErrorIs(t, p.Answer(id, Answer{Allow: true}), ErrNoSuchQuestion, "a late approval of a timed-out question is refused")
	assert.Zero(t, p.Waiting())
}

func TestANotificationFailureIsAnError(t *testing.T) {
	n := newNotes()
	n.err = errors.New("no channel")
	p := NewPending(n.notify)

	_, err := p.Ask(context.Background(), Request{Agent: "a", Tool: "Bash"}, `{}`)
	assert.ErrorContains(t, err, "no channel")
	assert.Zero(t, p.Waiting(), "nothing is left waiting for an operator who was never told")
}

func TestLongInputsAreClippedInTheQuestion(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	ask(p, context.Background(), "a", "Bash", strings.Repeat("x", 5000))
	waitSent(t, n)
	assert.Less(t, len(n.last()), 1500)
	assert.Contains(t, n.last(), "…")
}

func TestTwoAnswersInARowTheSecondIsRefusedImmediately(t *testing.T) {
	n := newNotes()
	p := NewPending(n.notify)
	res := ask(p, context.Background(), "a", "Bash", `{}`)
	waitSent(t, n)
	id := idIn(t, n.last())

	// Both answers arrive before the waiter has woken: the first is taken, the
	// second must be refused rather than overwrite it or block.
	done := make(chan error, 1)
	go func() {
		if err := p.Answer(id, Answer{Allow: true}); err != nil {
			done <- err
			return
		}
		done <- p.Answer(id, Answer{Allow: false})
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrNoSuchQuestion)
	case <-time.After(2 * time.Second):
		t.Fatal("the second answer blocked")
	}
	assert.True(t, (<-res).Allow, "the first answer stands")
}
