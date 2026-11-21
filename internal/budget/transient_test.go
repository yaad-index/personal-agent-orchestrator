package budget

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestARateLimitOrServerErrorIsTransientAndARefusalIsNot(t *testing.T) {
	for name, c := range map[string]struct {
		status    int
		header    string
		transient bool
		after     time.Duration
	}{
		"429 with seconds": {429, "120", true, 2 * time.Minute},
		"429 bare":         {429, "", true, 0},
		"503":              {503, "", true, 0},
		"500":              {500, "", true, 0},
		"401":              {401, "", false, 0},
		"403":              {403, "", false, 0},
		"404":              {404, "", false, 0},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(c.status)
		}))
		_, err := ClaudeSubscription{Credentials: credsFile(t, secret), URL: srv.URL}.Read(context.Background())
		srv.Close()
		require.Error(t, err, name)
		var tr *TransientError
		assert.Equal(t, c.transient, errors.As(err, &tr), name)
		if c.transient {
			assert.Equal(t, c.after, tr.RetryAfter, name)
		}
		assert.NotContains(t, err.Error(), secret, name)
	}
	srv, _ := usageServer(t, 200, usage)
	srv.Close()
	_, err := ClaudeSubscription{Credentials: credsFile(t, secret), URL: srv.URL}.Read(context.Background())
	var tr *TransientError
	assert.True(t, errors.As(err, &tr), "no connection passes too")
}

func TestRetryAfterIsReadInSecondsOrAsADate(t *testing.T) {
	now := time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)
	assert.Equal(t, 90*time.Second, retryAfter("90", now))
	assert.Equal(t, 5*time.Minute, retryAfter(now.Add(5*time.Minute).Format(http.TimeFormat), now))
	for _, v := range []string{"", "-3", "0", "soon", now.Add(-time.Minute).Format(http.TimeFormat)} {
		assert.Zero(t, retryAfter(v, now), v)
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type countingReader struct {
	fakeReader
	reads int
}

func (r *countingReader) Read(ctx context.Context) (Figure, error) {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.fakeReader.Read(ctx)
}

func transientBook(t *testing.T, r Reader) (*Book, *notices, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)}
	n := &notices{}
	b := New([]config.Account{metered("sub")}, func(config.Account) (Reader, error) { return r, nil },
		WithClock(c.Now), WithMaxStale(15*time.Minute),
		WithNotices(func(s Status) { n.faults = append(n.faults, s.Account+": "+s.Fault) }, func(s Status) { n.clears = append(n.clears, s.Account) }))
	b.every = 5 * time.Minute
	return b, n, c
}

func TestATransientFailureKeepsTheLastFigureAndTellsNoOne(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, n, c := transientBook(t, r)
	ctx := context.Background()
	b.Poll(ctx)
	good := b.Status("sub")

	r.set(Figure{}, &TransientError{Err: errors.New("usage endpoint answered HTTP 429")})
	c.add(5 * time.Minute)
	b.Poll(ctx)
	s := b.Status("sub")
	assert.Equal(t, Readable, s.State, "a rate limit is not a fault")
	assert.Equal(t, good.Figure, s.Figure, "the last good figure is kept")
	assert.Equal(t, good.ReadAt, s.ReadAt, "and says when it was read")
	assert.Equal(t, "usage endpoint answered HTTP 429", s.Retrying)
	assert.Equal(t, c.Now().Add(5*time.Minute), s.RetryAt)
	assert.True(t, b.Headroom("sub", core.BudgetRule{}), "judged on the kept figure")
	assert.Empty(t, n.faults, "no one is told of a failure that passes")

	r.set(fig(70, 50), nil)
	c.add(5 * time.Minute)
	b.Poll(ctx)
	s = b.Status("sub")
	assert.Equal(t, Readable, s.State)
	assert.InDelta(t, 70, s.Figure.Week.Left, 1e-9)
	assert.Empty(t, s.Retrying, "a good read clears it")
	assert.Empty(t, n.clears, "and there was no fault to clear")
}

func TestAKeptFigureOlderThanTheLimitIsAFaultTheMomentItIs(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, n, c := transientBook(t, r)
	ctx := context.Background()
	b.Poll(ctx) // good, at t0
	// A server error that asks for an hour: no read is due again until t0+1h.
	r.set(Figure{}, &TransientError{Err: errors.New("usage endpoint answered HTTP 503"), RetryAfter: time.Hour})
	c.add(time.Minute)
	b.Poll(ctx)
	require.Equal(t, Readable, b.Status("sub").State)

	c.add(14 * time.Minute) // t0+15m: at the limit, still kept
	assert.Equal(t, Readable, b.Status("sub").State)
	assert.True(t, b.Headroom("sub", core.BudgetRule{}))

	c.add(time.Second) // past it, with no read since
	s := b.Status("sub")
	assert.Equal(t, Fault, s.State, "a figure older than the limit is no longer the budget")
	assert.Equal(t, "the last good figure, read 2026-11-21T19:00:00Z, is older than 15m0s, and reads still fail: usage endpoint answered HTTP 503", s.Fault)
	assert.Equal(t, c.Now().Add(-time.Second), s.Since, "a fault since the moment the figure expired")
	assert.False(t, b.Headroom("sub", core.BudgetRule{}), "and grants no headroom, read or no read")
	assert.Empty(t, n.faults, "told at the next poll")

	reads := r.reads
	b.Poll(ctx)
	assert.Equal(t, reads, r.reads, "the endpoint's hour is still honoured")
	assert.Equal(t, []string{"sub: " + s.Fault}, n.faults, "the poll tells of it without waiting for a read")
	b.Poll(ctx)
	assert.Len(t, n.faults, 1, "once")

	r.set(fig(70, 50), nil)
	c.add(time.Hour)
	b.Poll(ctx)
	assert.Equal(t, Readable, b.Status("sub").State)
	assert.Equal(t, []string{"sub"}, n.clears, "and the clear is told when it reads again")
}

func TestAKeptFigureFaultsByTheLimitEvenWhenReadsKeepFailing(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, n, c := transientBook(t, r)
	ctx := context.Background()
	b.Poll(ctx)
	r.set(Figure{}, &TransientError{Err: errors.New("usage endpoint answered HTTP 503")})
	for range 20 {
		c.add(time.Minute)
		b.Poll(ctx)
		if c.Now().Sub(time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)) > 15*time.Minute {
			assert.Equal(t, Fault, b.Status("sub").State, "at %s", c.Now())
		}
	}
	assert.Len(t, n.faults, 1)
}

func TestATransientFailureWithNoGoodFigureIsAFault(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{err: &TransientError{Err: errors.New("usage endpoint answered HTTP 429")}}}
	b, n, _ := transientBook(t, r)
	b.Poll(context.Background())
	assert.Equal(t, Fault, b.Status("sub").State, "nothing to keep: no figure is never headroom")
	assert.Len(t, n.faults, 1)
}

func TestARefusalIsAFaultAtOnceDespiteAGoodFigure(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, n, c := transientBook(t, r)
	b.Poll(context.Background())
	r.set(Figure{}, errors.New("usage endpoint refused the login's token (HTTP 401)"))
	c.add(time.Minute)
	b.Poll(context.Background())
	assert.Equal(t, Fault, b.Status("sub").State)
	assert.Len(t, n.faults, 1)
}

func TestReadsBackOffAfterTransientFailures(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, _, c := transientBook(t, r)
	b.maxStale = 24 * time.Hour
	ctx := context.Background()
	b.Poll(ctx)
	r.set(Figure{}, &TransientError{Err: errors.New("429")})
	var waits []time.Duration
	last := c.Now()
	for len(waits) < 5 {
		c.add(time.Minute)
		before := r.reads
		b.Poll(ctx)
		if r.reads > before {
			waits = append(waits, c.Now().Sub(last))
			last = c.Now()
		}
	}
	// The first failure is at the first read after the good one; each wait
	// after it doubles from the poll interval, to the cap.
	assert.Equal(t, []time.Duration{time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute}, waits)

	r.set(Figure{}, &TransientError{Err: errors.New("429"), RetryAfter: 45 * time.Minute})
	c.add(30 * time.Minute)
	b.Poll(ctx)
	assert.Equal(t, c.Now().Add(45*time.Minute), b.Status("sub").RetryAt, "never sooner than the endpoint asked")

	r.set(fig(70, 50), nil)
	c.add(45 * time.Minute)
	b.Poll(ctx)
	r.set(Figure{}, &TransientError{Err: errors.New("429")})
	c.add(time.Minute)
	b.Poll(ctx)
	assert.Equal(t, c.Now().Add(5*time.Minute), b.Status("sub").RetryAt, "a good read starts the back-off over")
}

func TestAZeroStaleLimitKeepsTheDefault(t *testing.T) {
	b := New(nil, nil, WithMaxStale(0))
	assert.Equal(t, DefaultMaxStale, b.maxStale)
	assert.Equal(t, time.Minute, New(nil, nil, WithMaxStale(time.Minute)).maxStale)
}

func TestAnExpiredFigureIsAFaultInTheListingToo(t *testing.T) {
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, _, c := transientBook(t, r)
	b.Poll(context.Background())
	r.set(Figure{}, &TransientError{Err: errors.New("429"), RetryAfter: time.Hour})
	c.add(time.Minute)
	b.Poll(context.Background())
	c.add(15 * time.Minute)
	all := b.All()
	require.Len(t, all, 1)
	assert.Equal(t, Fault, all[0].State, "pao budget shows what dispatch sees")
}

func TestOnlyAFigureKeptThroughFailuresAges(t *testing.T) {
	// A figure read well is renewed on every poll, and the poll is never longer
	// than the limit; one not renewed because no poll came is not a failure to
	// read, and a poll that runs a little late must not flap it to a fault.
	r := &countingReader{fakeReader: fakeReader{f: fig(80, 60)}}
	b, n, c := transientBook(t, r)
	b.Poll(context.Background())
	c.add(16 * time.Minute)
	assert.Equal(t, Readable, b.Status("sub").State)
	assert.Empty(t, n.faults)
}
