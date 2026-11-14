package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrNoSuchQuestion means an answer named a question that is not waiting (it
// was already answered, or it timed out).
var ErrNoSuchQuestion = errors.New("no such pending question")

// Pending is the Asker that puts a question to the operator through a notify
// function and waits for an answer to arrive by id. The operator answers over
// whatever front they use (the local channel's decision endpoint is the
// button, ADR-0010 point 5).
type Pending struct {
	notify func(ctx context.Context, text string) error

	mu      sync.Mutex
	node    string
	seq     int
	waiting map[string]chan Answer
}

var _ Asker = (*Pending)(nil)

// NewPending returns a Pending that announces questions through notify.
func NewPending(notify func(ctx context.Context, text string) error) *Pending {
	return &Pending{notify: notify, waiting: make(map[string]chan Answer)}
}

// SetNode makes the questions asked from now on carry this node's id, "<node>.q3"
// (ADR-0017, G2), so an answer that reaches the master can be sent to the node that
// asked. Without it, as on a node that is not in a mesh, an id is "q3".
func (p *Pending) SetNode(id string) {
	p.mu.Lock()
	p.node = id
	p.mu.Unlock()
}

// Node returns the node id the questions carry, or "" when they carry none.
func (p *Pending) Node() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.node
}

// Ask announces the question and blocks until it is answered, ctx ends, or the
// notification cannot be sent. The text names the agent and the action, and how
// to answer.
func (p *Pending) Ask(ctx context.Context, req Request, canonicalInput string) (Answer, error) {
	p.mu.Lock()
	p.seq++
	id := fmt.Sprintf("q%d", p.seq)
	if p.node != "" {
		id = p.node + "." + id
	}
	ch := make(chan Answer, 1)
	p.waiting[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, id)
		p.mu.Unlock()
	}()

	text := fmt.Sprintf("agent %q wants to run %s: %s\nanswer:  /allow %s   |   /always %s  (allow and remember)   |   /deny %s [reason]",
		req.Agent, req.Tool, clip(canonicalInput, 1000), id, id, id)
	if err := p.notify(ctx, text); err != nil {
		return Answer{}, err
	}

	select {
	case ans := <-ch:
		return ans, nil
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// Answer delivers the operator's answer to the question with the given id, in the
// form it was asked: another node's question is not found here.
func (p *Pending) Answer(id string, ans Answer) error {
	p.mu.Lock()
	ch, ok := p.waiting[id]
	if ok {
		delete(p.waiting, id)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchQuestion, id)
	}
	ch <- ans // buffered; the waiter may already have given up, which is harmless
	return nil
}

// Waiting returns how many questions are waiting for an answer.
func (p *Pending) Waiting() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiting)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
