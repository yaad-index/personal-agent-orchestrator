package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// IdentityCmd groups the commands that manage who the humans are and which
// sender ids are theirs.
type IdentityCmd struct {
	Subjects SubjectsCmd      `cmd:"" help:"List the human subjects."`
	Add      SubjectAddCmd    `cmd:"" help:"Create a human subject."`
	Operator OperatorCmd      `cmd:"" help:"Flag a subject as acting as the operator, or clear the flag."`
	Remove   SubjectRemoveCmd `cmd:"" help:"Delete a subject and every binding that resolves to it."`
	Bindings BindingsCmd      `cmd:"" help:"List the bindings, of one subject or of all."`
	Bind     BindCmd          `cmd:"" help:"Tie a channel's sender id to a subject."`
	Revoke   RevokeCmd        `cmd:"" help:"Remove a binding; the sender id is unbound again."`
}

// SubjectsCmd lists subjects.
type SubjectsCmd struct{ NodeFlags }

// Run implements identity subjects.
func (c SubjectsCmd) Run() error { return c.run(os.Stdout) }

func (c SubjectsCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	subs, err := cl.Subjects(ctx)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Created.UTC().Format("2006-01-02T15:04:05Z"), flagWord(s.Operator)); err != nil {
			return err
		}
	}
	return nil
}

// SubjectAddCmd creates a subject.
type SubjectAddCmd struct {
	NodeFlags
	Name string `arg:"" help:"Subject name: lowercase letters, digits, '-' and '_', at most 32."`
}

// Run implements identity add.
func (c SubjectAddCmd) Run() error { return c.run(os.Stdout) }

func (c SubjectAddCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.AddSubject(ctx, c.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "subject %s added\n", c.Name)
	return err
}

// SubjectRemoveCmd deletes a subject.
type SubjectRemoveCmd struct {
	NodeFlags
	Name string `arg:"" help:"Subject to delete."`
}

// Run implements identity remove.
func (c SubjectRemoveCmd) Run() error { return c.run(os.Stdout) }

func (c SubjectRemoveCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.RemoveSubject(ctx, c.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "subject %s removed with its bindings\n", c.Name)
	return err
}

// BindingsCmd lists bindings.
type BindingsCmd struct {
	NodeFlags
	Subject string `help:"Only the bindings of this subject."`
}

// Run implements identity bindings.
func (c BindingsCmd) Run() error { return c.run(os.Stdout) }

func (c BindingsCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	bs, err := cl.Bindings(ctx, c.Subject)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", b.Channel, b.Sender, b.Subject); err != nil {
			return err
		}
	}
	return nil
}

// BindCmd binds a sender id to a subject.
type BindCmd struct {
	NodeFlags
	Channel string `arg:"" help:"Channel name."`
	Sender  string `arg:"" help:"The platform's own id for the sender on that channel."`
	Subject string `arg:"" help:"Subject the sender resolves to."`
}

// Run implements identity bind.
func (c BindCmd) Run() error { return c.run(os.Stdout) }

func (c BindCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.Bind(ctx, bindingOf(c.Channel, c.Sender, c.Subject)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s %s bound to %s\n", c.Channel, c.Sender, c.Subject)
	return err
}

// RevokeCmd removes a binding.
type RevokeCmd struct {
	NodeFlags
	Channel string `arg:"" help:"Channel name."`
	Sender  string `arg:"" help:"The sender id to unbind."`
}

// Run implements identity revoke.
func (c RevokeCmd) Run() error { return c.run(os.Stdout) }

func (c RevokeCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.Revoke(ctx, c.Channel, c.Sender); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s %s unbound\n", c.Channel, c.Sender)
	return err
}

func bindingOf(channel, sender, subject string) local.BindingJSON {
	return local.BindingJSON{Channel: channel, Sender: sender, Subject: subject}
}

// InviteCmd issues an invitation.
type InviteCmd struct {
	NodeFlags
	Subject string        `arg:"" help:"Subject the invitation registers its holder as."`
	TTL     time.Duration `help:"How long the invitation lasts. Default 15m." default:"0s"`
}

// Run implements identity invite.
func (c InviteCmd) Run() error { return c.run(os.Stdout) }

func (c InviteCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	inv, err := cl.Invite(ctx, c.Subject, c.TTL)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "invitation %s for %s, valid until %s\nsecret (shown once): %s\n", inv.ID, inv.Subject, inv.Expires.UTC().Format("2006-01-02T15:04:05Z"), inv.Secret)
	return err
}

// InvitesCmd lists invitations.
type InvitesCmd struct{ NodeFlags }

// Run implements identity invites.
func (c InvitesCmd) Run() error { return c.run(os.Stdout) }

func (c InvitesCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	invs, err := cl.Invitations(ctx)
	if err != nil {
		return err
	}
	for _, i := range invs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", i.ID, i.Subject, i.Expires.UTC().Format("2006-01-02T15:04:05Z")); err != nil {
			return err
		}
	}
	return nil
}

// UninviteCmd revokes an invitation.
type UninviteCmd struct {
	NodeFlags
	ID string `arg:"" help:"Invitation id, as invites lists it."`
}

// Run implements identity uninvite.
func (c UninviteCmd) Run() error { return c.run(os.Stdout) }

func (c UninviteCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.RevokeInvitation(ctx, c.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "invitation %s revoked\n", c.ID)
	return err
}

func flagWord(op bool) string {
	if op {
		return "operator"
	}
	return "-"
}

// OperatorCmd flags or unflags an operator subject.
type OperatorCmd struct {
	NodeFlags
	Name string `arg:"" help:"Subject to flag."`
	Off  bool   `help:"Clear the flag instead of setting it."`
}

// Run implements identity operator.
func (c OperatorCmd) Run() error { return c.run(os.Stdout) }

func (c OperatorCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.SetOperator(ctx, c.Name, !c.Off); err != nil {
		return err
	}
	if c.Off {
		_, err = fmt.Fprintf(w, "subject %s is no longer an operator\n", c.Name)
	} else {
		_, err = fmt.Fprintf(w, "subject %s now acts as the operator\n", c.Name)
	}
	return err
}
