package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yaad-index/personal-agent-orchestrator/internal/budget"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// accountReader builds the reader a metered account's figure is read by.
func accountReader(a config.Account) (budget.Reader, error) {
	switch a.Reader {
	case config.ReaderClaudeSubscription:
		return budget.ClaudeSubscription{Credentials: a.Credentials}, nil
	default:
		return nil, fmt.Errorf("no reader %q in this build", a.Reader)
	}
}

// newBudget returns the node's budget book. An account entering a fault, and
// leaving one, is told to each operator privately (ADR-0035 point 3), never to
// a group: it is the operator's to act on.
// operatorNotifier is all the budget may tell anyone through: each operator,
// privately. Taking no more than this, it cannot reach a group by mistake.
type operatorNotifier interface {
	NotifyOperators(ctx context.Context, m core.Message) (core.Message, error)
}

var _ operatorNotifier = (*comm.Layer)(nil)

func newBudget(ctx context.Context, cfg *config.Config, layer operatorNotifier, reader func(config.Account) (budget.Reader, error)) *budget.Book {
	if reader == nil {
		reader = accountReader
	}
	tell := func(text string) {
		if _, err := layer.NotifyOperators(ctx, core.Message{Sender: "pao", Text: text}); err != nil {
			slog.Warn("could not tell the operator of a budget fault", "error", err)
		}
	}
	return budget.New(cfg.Orchestrator.Accounts, reader, budget.WithMaxStale(cfg.Orchestrator.Budget.MaxStale), budget.WithNotices(
		func(s budget.Status) {
			tell(fmt.Sprintf("budget: account %q cannot be read (%s). No new work is dispatched to its agents until it reads again; running work is not stopped.", s.Account, s.Fault))
		},
		func(s budget.Status) {
			tell(fmt.Sprintf("budget: account %q reads again: week %.0f%% left, five-hour %.0f%% left.", s.Account, s.Figure.Week.Left, s.Figure.FiveHour.Left))
		},
	))
}

// budgetJSON is every account's budget in the local channel's shape. Headroom
// is judged by the zero rule, every window above 0: an agent's own rule may be
// stricter.
func budgetJSON(b *budget.Book) func() []local.BudgetJSON {
	return func() []local.BudgetJSON {
		var out []local.BudgetJSON
		for _, s := range b.All() {
			j := local.BudgetJSON{Account: s.Account, State: string(s.State), Fault: s.Fault, Retrying: s.Retrying, RetryAt: s.RetryAt, Since: s.Since, Headroom: b.Headroom(s.Account, core.BudgetRule{})}
			if s.State == budget.Readable {
				j.Week = &local.BudgetWindowJSON{Left: s.Figure.Week.Left, Resets: s.Figure.Week.Resets}
				j.FiveHour = &local.BudgetWindowJSON{Left: s.Figure.FiveHour.Left, Resets: s.Figure.FiveHour.Resets}
				j.ReadAt = s.ReadAt
			}
			out = append(out, j)
		}
		return out
	}
}
