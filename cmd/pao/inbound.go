package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// InboundCmd groups the commands that inspect what pao received and did not act on.
type InboundCmd struct {
	Rejected RejectedCmd `cmd:"" help:"List the inbounds pao did not act on, most recent first."`
}

// RejectedCmd lists the rejected-inbound record.
type RejectedCmd struct{ NodeFlags }

// Run implements inbound rejected.
func (c RejectedCmd) Run() error { return c.run(os.Stdout) }

func (c RejectedCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	es, err := cl.Rejected(ctx)
	if err != nil {
		return err
	}
	for _, e := range es {
		// The payload is stranger content: quoted, so a newline or a control
		// character in it cannot pass for another row or move the cursor.
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\tx%d\t%s\t%s\n", e.Last.UTC().Format("2006-01-02T15:04:05Z"),
			e.Channel, strconv.Quote(e.Sender), e.Kind, e.Count, strconv.Quote(e.Reason), strconv.Quote(e.Payload)); err != nil {
			return err
		}
	}
	return nil
}
