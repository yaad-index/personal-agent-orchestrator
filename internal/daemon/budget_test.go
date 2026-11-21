package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/budget"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

type switchReader struct {
	mu  sync.Mutex
	err error
}

func (r *switchReader) fail(err error) { r.mu.Lock(); r.err = err; r.mu.Unlock() }
func (r *switchReader) Read(context.Context) (budget.Figure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return budget.Figure{}, r.err
	}
	return budget.Figure{Week: budget.Window{Left: 93}, FiveHour: budget.Window{Left: 54}}, nil
}

func budgetOf(t *testing.T, n *node, account string) local.BudgetJSON {
	t.Helper()
	all, err := n.client.Budget(context.Background())
	require.NoError(t, err)
	for _, b := range all {
		if b.Account == account {
			return b
		}
	}
	t.Fatalf("no account %q in %v", account, all)
	return local.BudgetJSON{}
}

func TestTheNodeReadsItsAccountsAndTellsTheOperatorOfAFault(t *testing.T) {
	yes, no := true, false
	r := &switchReader{}
	n := startNodeWith(t, map[string]string{"a": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Accounts = []config.Account{
			{Name: "sub", Metered: &yes, Reader: config.ReaderClaudeSubscription, Credentials: "/unused"},
			{Name: "local", Metered: &no},
		}
		cfg.Orchestrator.Budget.Poll = 20 * time.Millisecond
	}, func(o *Options) {
		o.BudgetReader = func(a config.Account) (budget.Reader, error) {
			require.Equal(t, "sub", a.Name, "an unmetered account is never given a reader")
			return r, nil
		}
	})

	assert.Equal(t, local.BudgetJSON{Account: "local", State: "unmetered", Since: budgetOf(t, n, "local").Since, Headroom: true}, budgetOf(t, n, "local"))
	require.Eventually(t, func() bool { return budgetOf(t, n, "sub").State == "readable" }, 5*time.Second, 10*time.Millisecond)
	sub := budgetOf(t, n, "sub")
	require.NotNil(t, sub.Week)
	assert.InDelta(t, 93, sub.Week.Left, 1e-9)
	assert.InDelta(t, 54, sub.FiveHour.Left, 1e-9)
	assert.True(t, sub.Headroom)

	r.fail(errors.New("usage endpoint answered HTTP 500"))
	got := n.next(t)
	assert.Equal(t, "pao", got.Sender)
	assert.Contains(t, got.Text, `account "sub" cannot be read (usage endpoint answered HTTP 500)`)
	assert.Contains(t, got.Text, "running work is not stopped")
	sub = budgetOf(t, n, "sub")
	assert.Equal(t, "fault", sub.State)
	assert.False(t, sub.Headroom)
	assert.Nil(t, sub.Week, "a fault shows no figure")

	r.fail(nil)
	got = n.next(t)
	assert.True(t, strings.HasPrefix(got.Text, `budget: account "sub" reads again`), got.Text)
	assert.Equal(t, "readable", budgetOf(t, n, "sub").State)
}

func TestAnAccountWithNoReaderInThisBuildIsAFaultToldAtStart(t *testing.T) {
	yes := true
	n := startNodeWith(t, map[string]string{"a": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Accounts = []config.Account{{Name: "sub", Metered: &yes, Reader: "elsewhere"}}
	})
	got := n.next(t)
	assert.Contains(t, got.Text, `account "sub" cannot be read (no reader "elsewhere" in this build)`)
	assert.Equal(t, "fault", budgetOf(t, n, "sub").State)
}
