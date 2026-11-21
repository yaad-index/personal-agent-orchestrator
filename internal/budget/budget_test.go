package budget

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

const secret = "sk-ant-oat-SECRET-TOKEN"

func credsFile(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"claudeAiOauth":{"accessToken":"`+token+`","refreshToken":"r","expiresAt":1}}`), 0o600))
	return p
}

func usageServer(t *testing.T, status int, body string) (*httptest.Server, *http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

const usage = `{"five_hour":{"utilization":46.0,"resets_at":"2026-11-21T18:19:59.634255+00:00"},"seven_day":{"utilization":7.0,"resets_at":"2026-11-28T12:59:59.634279+00:00"},"extra_usage":{"is_enabled":false}}`

func TestTheSubscriptionReaderReadsTheShareLeftOfEachWindow(t *testing.T) {
	srv, hdr := usageServer(t, 200, usage)
	f, err := ClaudeSubscription{Credentials: credsFile(t, secret), URL: srv.URL}.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+secret, hdr.Get("Authorization"))
	assert.Equal(t, "oauth-2025-04-20", hdr.Get("anthropic-beta"))
	assert.InDelta(t, 93.0, f.Week.Left, 1e-9, "left is 100 minus the share used")
	assert.InDelta(t, 54.0, f.FiveHour.Left, 1e-9)
	assert.Equal(t, time.Date(2026, 11, 28, 12, 59, 59, 634279000, time.UTC), f.Week.Resets.UTC())
}

func TestTheSubscriptionReaderReadsTheTokenAfreshEachTime(t *testing.T) {
	srv, hdr := usageServer(t, 200, usage)
	path := credsFile(t, "first")
	r := ClaudeSubscription{Credentials: path, URL: srv.URL}
	_, err := r.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer first", hdr.Get("Authorization"))
	// The harness refreshed its token and rewrote the file.
	require.NoError(t, os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"second"}}`), 0o600))
	_, err = r.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer second", hdr.Get("Authorization"))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"claudeAiOauth":{"accessToken":"second"}}`, string(b), "the reader never writes the file")
}

func TestTheSubscriptionReaderFailsWithoutGuessingOrLeakingTheToken(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		creds  func(t *testing.T) string
		want   string
	}{
		"refused token":    {401, `{"error":"` + secret + `"}`, nil, "refused the login's token (HTTP 401)"},
		"server error":     {500, secret, nil, "HTTP 500"},
		"not json":         {200, "<html>" + secret, nil, "not the expected JSON"},
		"no five-hour":     {200, `{"seven_day":{"utilization":7}}`, nil, "no five_hour utilization"},
		"null week":        {200, `{"five_hour":{"utilization":1},"seven_day":null}`, nil, "no seven_day utilization"},
		"null utilization": {200, `{"five_hour":{"utilization":null},"seven_day":{"utilization":7}}`, nil, "no five_hour utilization"},
		"over 100":         {200, `{"five_hour":{"utilization":101},"seven_day":{"utilization":7}}`, nil, "outside 0-100"},
		"bad reset":        {200, `{"five_hour":{"utilization":1,"resets_at":"soon"},"seven_day":{"utilization":7}}`, nil, "resets_at is not a time"},
		"no creds file":    {200, usage, func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.json") }, "credentials file"},
		"creds not json": {200, usage, func(t *testing.T) string {
			p := credsFile(t, "")
			require.NoError(t, os.WriteFile(p, []byte(secret), 0o600))
			return p
		}, "credentials file is not the expected JSON"},
		"no token": {200, usage, func(t *testing.T) string { return credsFile(t, "") }, "holds no access token"},
	} {
		srv, _ := usageServer(t, c.status, c.body)
		creds := credsFile(t, secret)
		if c.creds != nil {
			creds = c.creds(t)
		}
		_, err := ClaudeSubscription{Credentials: creds, URL: srv.URL}.Read(context.Background())
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
		assert.NotContains(t, err.Error(), secret, name)
		assert.NotContains(t, err.Error(), "SECRET", name)
	}
}

func TestATransportErrorDoesNotCarryTheRequest(t *testing.T) {
	srv, _ := usageServer(t, 200, usage)
	srv.Close()
	_, err := ClaudeSubscription{Credentials: credsFile(t, secret), URL: srv.URL + "/usage?x=1"}.Read(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage request failed")
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), "x=1", "the URL is left out")
}

type fakeReader struct {
	mu  sync.Mutex
	f   Figure
	err error
}

func (r *fakeReader) set(f Figure, err error) { r.mu.Lock(); r.f, r.err = f, err; r.mu.Unlock() }
func (r *fakeReader) Read(context.Context) (Figure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f, r.err
}

func fig(week, five float64) Figure {
	return Figure{Week: Window{Left: week}, FiveHour: Window{Left: five}}
}

type notices struct{ faults, clears []string }

func book(t *testing.T, readers map[string]*fakeReader, accounts ...config.Account) (*Book, *notices) {
	t.Helper()
	n := &notices{}
	b := New(accounts, func(a config.Account) (Reader, error) {
		if r, ok := readers[a.Name]; ok {
			return r, nil
		}
		return nil, errors.New("no reader for " + a.Name)
	}, WithNotices(func(s Status) { n.faults = append(n.faults, s.Account+": "+s.Fault) }, func(s Status) { n.clears = append(n.clears, s.Account) }))
	return b, n
}

func metered(name string) config.Account {
	yes := true
	return config.Account{Name: name, Metered: &yes, Reader: config.ReaderClaudeSubscription}
}

func unmetered(name string) config.Account {
	no := false
	return config.Account{Name: name, Metered: &no}
}

func TestAnUnmeteredAccountAlwaysHasHeadroomAndIsNeverRead(t *testing.T) {
	b, _ := book(t, nil, unmetered("local"))
	b.Poll(context.Background())
	assert.Equal(t, Unmetered, b.Status("local").State)
	assert.True(t, b.Headroom("local", core.BudgetRule{Floor: 99}))
}

func TestAMeteredAccountIsAtFaultUntilItsFirstRead(t *testing.T) {
	r := &fakeReader{f: fig(50, 50)}
	b, n := book(t, map[string]*fakeReader{"sub": r}, metered("sub"))
	assert.Equal(t, Fault, b.Status("sub").State)
	assert.Equal(t, "not read yet", b.Status("sub").Fault)
	assert.False(t, b.Headroom("sub", core.BudgetRule{}))
	b.Poll(context.Background())
	assert.Equal(t, Readable, b.Status("sub").State)
	assert.True(t, b.Headroom("sub", core.BudgetRule{}))
	assert.Empty(t, n.faults, "a first read that succeeds tells no one")
	assert.Empty(t, n.clears, "and clears nothing anyone was told of")
}

func TestAFailedReadIsAFaultToldOnceAndClearedByTheNextGoodRead(t *testing.T) {
	r := &fakeReader{f: fig(50, 50)}
	b, n := book(t, map[string]*fakeReader{"sub": r}, metered("sub"))
	ctx := context.Background()
	b.Poll(ctx)
	r.set(Figure{}, errors.New("endpoint down"))
	b.Poll(ctx)
	b.Poll(ctx)
	s := b.Status("sub")
	assert.Equal(t, Fault, s.State)
	assert.Equal(t, "endpoint down", s.Fault)
	assert.False(t, b.Headroom("sub", core.BudgetRule{}), "a fault is never headroom")
	assert.Equal(t, []string{"sub: endpoint down"}, n.faults, "told once, not every poll")
	assert.Equal(t, Figure{}, s.Figure, "no stale figure is kept to judge by")

	r.set(fig(40, 30), nil)
	b.Poll(ctx)
	assert.Equal(t, Readable, b.Status("sub").State)
	assert.Equal(t, []string{"sub"}, n.clears)
}

func TestAFirstReadThatFailsIsTold(t *testing.T) {
	r := &fakeReader{err: errors.New("refused")}
	b, n := book(t, map[string]*fakeReader{"sub": r}, metered("sub"))
	b.Poll(context.Background())
	assert.Equal(t, []string{"sub: refused"}, n.faults)
}

func TestAnAccountWhoseReaderCannotBeBuiltIsAtFaultAndToldWhenRun(t *testing.T) {
	b, n := book(t, nil, metered("sub"))
	assert.Equal(t, "no reader for sub", b.Status("sub").Fault)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.Run(ctx, time.Hour)
	assert.Equal(t, []string{"sub: no reader for sub"}, n.faults)
}

func TestNoAccountAndAnUnknownAccountAreFaultsNeverUnmetered(t *testing.T) {
	b, _ := book(t, nil, unmetered("local"))
	for _, name := range []string{"", "elsewhere"} {
		s := b.Status(name)
		assert.Equal(t, Fault, s.State, "%q", name)
		assert.False(t, b.Headroom(name, core.BudgetRule{}), "%q", name)
	}
	assert.Contains(t, b.Status("").Fault, "names no account")
	assert.Contains(t, b.Status("elsewhere").Fault, `"elsewhere" is not configured`)
}

func TestHeadroomIsJudgedByTheAgentsOwnRule(t *testing.T) {
	r := &fakeReader{f: fig(80, 10)}
	b, _ := book(t, map[string]*fakeReader{"sub": r}, metered("sub"))
	b.Poll(context.Background())
	for name, c := range map[string]struct {
		rule core.BudgetRule
		want bool
	}{
		"every window above 0":         {core.BudgetRule{}, true},
		"both windows above 20":        {core.BudgetRule{Floor: 20}, false},
		"the week alone above 20":      {core.BudgetRule{Windows: []string{core.WindowWeek}, Floor: 20}, true},
		"the five-hour alone above 20": {core.BudgetRule{Windows: []string{core.WindowFiveHour}, Floor: 20}, false},
		"at the floor is not above it": {core.BudgetRule{Windows: []string{core.WindowFiveHour}, Floor: 10}, false},
		"just under the floor":         {core.BudgetRule{Windows: []string{core.WindowFiveHour}, Floor: 9.9}, true},
		"the week alone above 80":      {core.BudgetRule{Windows: []string{core.WindowWeek}, Floor: 80}, false},
	} {
		assert.Equal(t, c.want, b.Headroom("sub", c.rule), name)
	}
	r.set(fig(0, 50), nil)
	b.Poll(context.Background())
	assert.False(t, b.Headroom("sub", core.BudgetRule{}), "a spent week is no headroom")
	assert.True(t, b.Headroom("sub", core.BudgetRule{Windows: []string{core.WindowFiveHour}}), "unless the rule weighs only the five hours")
}

func TestAllListsEveryAccountByName(t *testing.T) {
	b, _ := book(t, nil, unmetered("b"), metered("a"))
	var names []string
	for _, s := range b.All() {
		names = append(names, s.Account)
	}
	assert.Equal(t, "a,b", strings.Join(names, ","))
}
