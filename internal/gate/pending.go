package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// ErrNoSuchQuestion means an answer named a question that is not waiting (it
// was already answered, or it timed out).
var ErrNoSuchQuestion = errors.New("no such pending question")

// Question is a gate question as it is put to the operator.
type Question struct {
	ID string
	// Agent is the agent whose call raised the question.
	Agent string
	Text  string
	// Actions are the answers the operator may give, each with the typed command
	// it stands for (ADR-0027 point 1).
	Actions []core.Action
}

// Outcomes a settled question is closed with (ADR-0027 point 6).
const (
	OutcomeAllowed    = "allowed"
	OutcomeRemembered = "allowed and remembered"
	OutcomeDenied     = "denied"
	OutcomeExpired    = "expired"
)

// Notifier puts questions to the operator. Settled is told when a question
// announced under handle is no longer waiting, so what showed its actions can
// close them; it must not block the decision.
type Notifier interface {
	Announce(ctx context.Context, q Question) (handle string, err error)
	Settled(handle, outcome string)
}

// NotifyFunc is a Notifier that announces only the question's text and has
// nothing to close.
type NotifyFunc func(ctx context.Context, text string) error

// Announce sends the question's text.
func (f NotifyFunc) Announce(ctx context.Context, q Question) (string, error) {
	return "", f(ctx, q.Text)
}

// Settled does nothing.
func (NotifyFunc) Settled(string, string) {}

// Pending is the Asker that puts a question to the operator through a Notifier
// and waits for an answer to arrive by id. The operator answers over whatever
// front they use: a typed command, or a button that stands for one.
type Pending struct {
	notify Notifier

	mu      sync.Mutex
	node    string
	seq     int
	waiting map[string]chan Answer
}

var _ Asker = (*Pending)(nil)

// NewPending returns a Pending that announces questions through notify.
func NewPending(notify Notifier) *Pending {
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

	q := Question{
		ID:    id,
		Agent: req.Agent,
		Text: fmt.Sprintf("agent %q wants to run %s: %s\nanswer:  /pao allow %s   |   /pao always %s  (allow and remember)   |   /pao deny %s [reason]",
			req.Agent, req.Tool, clip(canonicalInput, 1000), id, id, id),
		Actions: []core.Action{
			{Label: "ALLOW", Command: "/pao allow " + id},
			{Label: "ALWAYS", Command: "/pao always " + id},
			{Label: "DENY", Command: "/pao deny " + id},
		},
	}
	handle, err := p.notify.Announce(ctx, q)
	if err != nil {
		if handle != "" {
			p.notify.Settled(handle, OutcomeExpired)
		}
		return Answer{}, err
	}

	select {
	case ans := <-ch:
		p.notify.Settled(handle, outcome(ans))
		return ans, nil
	case <-ctx.Done():
		p.notify.Settled(handle, OutcomeExpired)
		return Answer{}, ctx.Err()
	}
}

func outcome(a Answer) string {
	switch {
	case a.Allow && a.Remember:
		return OutcomeRemembered
	case a.Allow:
		return OutcomeAllowed
	}
	return OutcomeDenied
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
