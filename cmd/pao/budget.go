package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"
)

// BudgetCmd shows each account's budget as the running node last read it.
type BudgetCmd struct{ NodeFlags }

// Run implements budget.
func (c BudgetCmd) Run() error { return c.run(os.Stdout) }

func (c BudgetCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	accounts, err := cl.Budget(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		_, err := fmt.Fprintln(w, "no accounts configured")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, a := range accounts {
		var line string
		switch {
		case a.State == "readable" && a.Week != nil && a.FiveHour != nil:
			line = fmt.Sprintf("week %.0f%% left\tfive-hour %.0f%% left\tread %s", a.Week.Left, a.FiveHour.Left, a.ReadAt.Format(time.RFC3339))
		case a.State == "fault":
			line = fmt.Sprintf("FAULT since %s\t%s\t", a.Since.Format(time.RFC3339), a.Fault)
		default:
			line = "no budget\t\t"
		}
		headroom := "no headroom"
		if a.Headroom {
			headroom = "headroom"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Account, a.State, line, headroom); err != nil {
			return err
		}
	}
	return tw.Flush()
}
