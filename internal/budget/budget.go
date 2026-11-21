// Package budget keeps each account's budget figure (ADR-0035): the account's
// own limit and remaining, read from the provider, never estimated by pao. An
// account is unmetered, readable, or at fault; a metered account whose figure
// cannot be read is a fault, never a guess and never unlimited.
package budget

import (
	"context"
	"errors"
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
	// Retrying says why the last read failed when the failure was transient
	// (a rate limit, a server error, a broken connection) and the last good
	// figure is kept meanwhile; RetryAt is when it is read again.
	Retrying string
	RetryAt  time.Time
	// Since is when the account entered its state.
	Since time.Time
}

// TransientError is a read that failed for a reason that passes: the endpoint
// limited the rate, answered with a server error, or could not be reached. The
// last good figure is kept for a while, and the read tried again later.
type TransientError struct {
	Err error
	// RetryAfter is how long the endpoint asked to wait, if it said.
	RetryAfter time.Duration
}

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// The bounds on retrying a transient failure.
const (
	// DefaultMaxStale is how old a kept figure may grow before the account is at
	// fault, when no other is set.
	DefaultMaxStale = 15 * time.Minute
	// MaxBackoff caps how long a read waits after transient failures.
	MaxBackoff = 30 * time.Minute
)

// Book holds every account's status and keeps the metered ones read.
type Book struct {
	readers map[string]Reader
	now     func() time.Time
	// onFault is called when an account enters a fault, and onClear when it
	// leaves one.
	onFault func(Status)
	onClear func(Status)
	// maxStale is how old a kept figure may grow while reads fail transiently.
	maxStale time.Duration
	// every is the poll interval, the base of the back-off.
	every time.Duration

	mu       sync.Mutex
	statuses map[string]Status
	failures map[string]int
	nextRead map[string]time.Time
}

// Option configures a Book.
type Option func(*Book)

// WithClock sets the clock. The default is time.Now.
func WithClock(now func() time.Time) Option { return func(b *Book) { b.now = now } }

// WithMaxStale sets how old a kept figure may grow while reads fail
// transiently before the account is at fault. The default is DefaultMaxStale.
func WithMaxStale(d time.Duration) Option {
	return func(b *Book) {
		if d > 0 {
			b.maxStale = d
		}
	}
}

// WithNotices sets what is called when an account enters a fault and when it
// leaves one.
func WithNotices(onFault, onClear func(Status)) Option {
	return func(b *Book) { b.onFault, b.onClear = onFault, onClear }
}

// New returns a book of the given accounts. A metered account starts at fault,
// "not read yet", until its first read. newReader builds a metered account's
// reader; an error from it leaves that account at fault for good, saying why.
func New(accounts []config.Account, newReader func(config.Account) (Reader, error), opts ...Option) *Book {
	b := &Book{readers: map[string]Reader{}, now: time.Now, statuses: map[string]Status{}, maxStale: DefaultMaxStale, every: time.Minute,
		failures: map[string]int{}, nextRead: map[string]time.Time{}}
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

// Poll reads every metered account that is due. A read that fails for good
// puts the account at fault at once. A transient failure keeps the last good
// figure while it is no older than the stale limit, and puts off the next read
// of that account; past the limit, or with no good figure, it is a fault. The
// next read that succeeds clears either.
func (b *Book) Poll(ctx context.Context) {
	names := make([]string, 0, len(b.readers))
	for n := range b.readers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.expire(n)
		b.mu.Lock()
		due := !b.now().Before(b.nextRead[n])
		b.mu.Unlock()
		if !due {
			continue
		}
		f, err := b.readers[n].Read(ctx)
		b.record(n, f, err)
	}
}

// backoff is how long to wait after the nth transient failure in a row: the
// poll interval, doubled each time, capped, and never less than the endpoint
// asked for.
func (b *Book) backoff(n int, asked time.Duration) time.Duration {
	d := b.every
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 2
	}
	d = min(d, MaxBackoff)
	return max(d, asked)
}

func (b *Book) record(name string, f Figure, err error) {
	b.mu.Lock()
	prev := b.statuses[name]
	at := b.now()
	var transient *TransientError
	if errors.As(err, &transient) {
		b.failures[name]++
		b.nextRead[name] = at.Add(b.backoff(b.failures[name], transient.RetryAfter))
		if prev.State == Readable && at.Sub(prev.ReadAt) <= b.maxStale {
			kept := prev
			kept.Retrying, kept.RetryAt = err.Error(), b.nextRead[name]
			b.statuses[name] = kept
			b.mu.Unlock()
			slog.Info("budget: account read failed for now; the last figure is kept", "account", name, "why", err, "read_at", prev.ReadAt, "retry_at", kept.RetryAt)
			return
		}
	} else {
		delete(b.failures, name)
		delete(b.nextRead, name)
	}
	next := Status{Account: name, Since: prev.Since}
	if err != nil {
		next.State, next.Fault = Fault, err.Error()
		if transient != nil {
			next.RetryAt = b.nextRead[name]
		}
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
	b.mu.Lock()
	b.every = every
	b.mu.Unlock()
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
		return b.aged(s)
	}
	if account == "" {
		return Status{State: Fault, Fault: "the agent names no account"}
	}
	return Status{Account: account, State: Fault, Fault: fmt.Sprintf("account %q is not configured", account)}
}

// aged is s as of now: a figure kept through transient failures that has grown
// older than the stale limit is a fault from that moment, whether or not a read
// has been tried since (ADR-0035 point 3). The caller holds b.mu.
func (b *Book) aged(s Status) Status {
	if s.State != Readable || s.Retrying == "" {
		return s
	}
	expires := s.ReadAt.Add(b.maxStale)
	if !b.now().After(expires) {
		return s
	}
	return Status{Account: s.Account, State: Fault, Since: expires, RetryAt: s.RetryAt,
		Fault: fmt.Sprintf("the last good figure, read %s, is older than %s, and reads still fail: %s", s.ReadAt.Format(time.RFC3339), b.maxStale, s.Retrying)}
}

// expire makes the fault of a kept figure past the stale limit the stored
// state, and tells of it, without waiting for the next read.
func (b *Book) expire(name string) {
	b.mu.Lock()
	prev := b.statuses[name]
	next := b.aged(prev)
	if next.State == prev.State {
		b.mu.Unlock()
		return
	}
	b.statuses[name] = next
	b.mu.Unlock()
	slog.Warn("budget: account figure unreadable", "account", name, "why", next.Fault)
	if b.onFault != nil {
		b.onFault(next)
	}
}

// All returns every account's status, by name.
func (b *Book) All() []Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Status, 0, len(b.statuses))
	for _, s := range b.statuses {
		out = append(out, b.aged(s))
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
