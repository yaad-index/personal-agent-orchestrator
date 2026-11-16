package main

import (
	"fmt"
	"io"
	"os"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// IdentityCmd groups the commands that manage who the humans are and which
// sender ids are theirs.
type IdentityCmd struct {
	Subjects SubjectsCmd      `cmd:"" help:"List the human subjects."`
	Add      SubjectAddCmd    `cmd:"" help:"Create a human subject."`
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
		if _, err := fmt.Fprintf(w, "%s\t%s\n", s.Name, s.Created.UTC().Format("2006-01-02T15:04:05Z")); err != nil {
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
