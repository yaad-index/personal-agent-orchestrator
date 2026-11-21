// Package budget keeps each account's budget figure (ADR-0035): the account's
// own limit and remaining, read from the provider, never estimated by pao. An
// account is unmetered, readable, or at fault; a metered account whose figure
// cannot be read is a fault, never a guess and never unlimited.
package budget

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// State is what is known of an account's budget.
type State string

const (
	// Unmetered is an account declared to have no budget: its agents are never
	// held back for one (ADR-0035 point 2).
	Unmetered State = "unmetered"
	// Readable is a metered account whose figure was read.
	Readable State = "readable"
	// Fault is a metered account whose figure cannot be read, or a name that
	// is no configured account (ADR-0035 point 3).
	Fault State = "fault"
)

// Window is one of an account's limit windows: the share of it left, in
// percent, and when it resets.
type Window struct {
	Left   float64
	Resets time.Time
}

// Figure is what a reader read of an account.
type Figure struct {
	Week     Window
	FiveHour Window
}

// Reader reads one account's figure. Its error never carries a credential.
type Reader interface {
	Read(ctx context.Context) (Figure, error)
}

// Status is an account's budget as last known.
type Status struct {
	Account string
	State   State
	// Figure and ReadAt are set when State is Readable.
	Figure Figure
	ReadAt time.Time
	// Fault says why the figure cannot be read, when State is Fault.
	Fault string
	// Since is when the account entered its state.
	Since time.Time
}

// Book holds every account's status and keeps the metered ones read.
type Book struct {
	readers map[string]Reader
	now     func() time.Time
	// onFault is called when an account enters a fault, and onClear when it
	// leaves one.
	onFault func(Status)
	onClear func(Status)

	mu       sync.Mutex
	statuses map[string]Status
}

// Option configures a Book.
type Option func(*Book)

// WithClock sets the clock. The default is time.Now.
func WithClock(now func() time.Time) Option { return func(b *Book) { b.now = now } }

// WithNotices sets what is called when an account enters a fault and when it
// leaves one.
func WithNotices(onFault, onClear func(Status)) Option {
	return func(b *Book) { b.onFault, b.onClear = onFault, onClear }
}

// New returns a book of the given accounts. A metered account starts at fault,
// "not read yet", until its first read. newReader builds a metered account's
// reader; an error from it leaves that account at fault for good, saying why.
func New(accounts []config.Account, newReader func(config.Account) (Reader, error), opts ...Option) *Book {
	b := &Book{readers: map[string]Reader{}, now: time.Now, statuses: map[string]Status{}}
	for _, o := range opts {
		o(b)
	}
	at := b.now()
	for _, a := range accounts {
		if a.Metered == nil || !*a.Metered {
			b.statuses[a.Name] = Status{Account: a.Name, State: Unmetered, Since: at}
			continue
		}
		r, err := newReader(a)
		if err != nil {
			b.statuses[a.Name] = Status{Account: a.Name, State: Fault, Fault: err.Error(), Since: at}
			continue
		}
		b.readers[a.Name] = r
		b.statuses[a.Name] = Status{Account: a.Name, State: Fault, Fault: "not read yet", Since: at}
	}
	return b
}

// Poll reads every metered account once. A read that fails puts the account at
// fault at once; the next read that succeeds clears it.
func (b *Book) Poll(ctx context.Context) {
	names := make([]string, 0, len(b.readers))
	for n := range b.readers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := b.readers[n].Read(ctx)
		b.record(n, f, err)
	}
}

func (b *Book) record(name string, f Figure, err error) {
	b.mu.Lock()
	prev := b.statuses[name]
	at := b.now()
	next := Status{Account: name, Since: prev.Since}
	if err != nil {
		next.State, next.Fault = Fault, err.Error()
	} else {
		next.State, next.Figure, next.ReadAt = Readable, f, at
	}
	if next.State != prev.State {
		next.Since = at
	}
	b.statuses[name] = next
	b.mu.Unlock()

	switch {
	case next.State == Fault && (prev.State != Fault || prev.Fault == "not read yet"):
		slog.Warn("budget: account figure unreadable", "account", name, "why", next.Fault)
		if b.onFault != nil {
			b.onFault(next)
		}
	case next.State == Readable && prev.State == Fault && prev.Fault != "not read yet":
		slog.Info("budget: account figure readable again", "account", name)
		if b.onClear != nil {
			b.onClear(next)
		}
	}
}

// Run polls every interval until ctx ends, starting at once.
func (b *Book) Run(ctx context.Context, every time.Duration) {
	// An account whose reader could not be built is at fault for good: say so
	// once, as a read that fails would.
	for _, s := range b.All() {
		if _, reads := b.readers[s.Account]; s.State == Fault && !reads {
			slog.Warn("budget: account figure unreadable", "account", s.Account, "why", s.Fault)
			if b.onFault != nil {
				b.onFault(s)
			}
		}
	}
	if len(b.readers) == 0 {
		return
	}
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		b.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Status returns an account's status. A name that is no configured account,
// the empty name of an agent that names none among them, is at fault: an agent
// with no account has no readable budget, never an unmetered one.
func (b *Book) Status(account string) Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.statuses[account]; ok {
		return s
	}
	if account == "" {
		return Status{State: Fault, Fault: "the agent names no account"}
	}
	return Status{Account: account, State: Fault, Fault: fmt.Sprintf("account %q is not configured", account)}
}

// All returns every account's status, by name.
func (b *Book) All() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Status, 0, len(b.statuses))
	for _, s := range b.statuses {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out
}

// Headroom reports whether work may be dispatched to an agent drawing on the
// account, judged by the agent's own rule: an unmetered account always, a
// readable one when every window the rule weighs keeps more than its floor, an
// account at fault never (ADR-0035, ADR-0036 point 3).
func (b *Book) Headroom(account string, rule core.BudgetRule) bool {
	s := b.Status(account)
	switch s.State {
	case Unmetered:
		return true
	case Readable:
		for w, win := range map[string]Window{core.WindowWeek: s.Figure.Week, core.WindowFiveHour: s.Figure.FiveHour} {
			if rule.Weighs(w) && win.Left <= rule.Floor {
				return false
			}
		}
		return true
	default:
		return false
	}
}
