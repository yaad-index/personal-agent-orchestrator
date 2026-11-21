package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

// TasksCmd groups the commands on tasks, the units of work pao dispatches.
type TasksCmd struct {
	List   TasksListCmd   `cmd:"" help:"List the tasks, newest first."`
	Show   TasksShowCmd   `cmd:"" help:"Show one task."`
	Create TasksCreateCmd `cmd:"" help:"Create a task, owned by the operator."`
	Cancel TasksCancelCmd `cmd:"" help:"Cancel a task that is not over."`
}

// TasksCancelCmd cancels a task as the operator.
type TasksCancelCmd struct {
	NodeFlags
	ID     string `arg:"" help:"The task's id."`
	Reason string `help:"Why it is canceled."`
}

// Run implements tasks cancel.
func (c TasksCancelCmd) Run() error { return c.run(os.Stdout) }

func (c TasksCancelCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	t, err := cl.CancelTask(ctx, c.ID, c.Reason)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s %s\n", t.ID, t.Status)
	return err
}

// TasksListCmd lists the tasks.
type TasksListCmd struct{ NodeFlags }

// Run implements tasks list.
func (c TasksListCmd) Run() error { return c.run(os.Stdout) }

func (c TasksListCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	ts, err := cl.Tasks(ctx)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		_, err := fmt.Fprintln(w, "no tasks")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, t := range ts {
		assignee := t.Assignee
		if assignee == "" {
			assignee = "-"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Folder, t.Status, strings.Join(t.Tags, ","), assignee, t.Title); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// TasksShowCmd shows one task.
type TasksShowCmd struct {
	NodeFlags
	ID string `arg:"" help:"The task's id."`
}

// Run implements tasks show.
func (c TasksShowCmd) Run() error { return c.run(os.Stdout) }

func (c TasksShowCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	t, err := cl.Task(ctx, c.ID)
	if err != nil {
		return err
	}
	return printTask(w, t)
}

func printTask(w io.Writer, t local.TaskJSON) error {
	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-12s %s\n", k+":", v)
		}
	}
	line("id", t.ID)
	line("title", t.Title)
	line("status", string(t.Status))
	line("folder", t.Folder)
	line("tags", strings.Join(t.Tags, ", "))
	line("owner", t.Owner)
	line("assignee", t.Assignee)
	line("parent", t.Parent)
	line("how", t.How)
	line("tools", strings.Join(t.Attachments.Tools, ", "))
	line("scripts", strings.Join(t.Attachments.Scripts, ", "))
	line("secret", t.Attachments.Secret)
	line("created", t.Created.Format(time.RFC3339))
	line("updated", t.Updated.Format(time.RFC3339))
	fmt.Fprintf(&b, "\n%s\n\ndone when: %s\n", t.Description, t.Done)
	if len(t.Log) > 0 || t.Dropped > 0 {
		b.WriteString("\nlog:\n")
		if t.Dropped > 0 {
			fmt.Fprintf(&b, "  (%d earlier entries let go)\n", t.Dropped)
		}
		for _, e := range t.Log {
			what := e.Kind
			if e.Kind == task.EntryStatus {
				what = "status " + string(e.Status)
			}
			fmt.Fprintf(&b, "  %s  %s  %s", e.At.Format(time.RFC3339), e.By, what)
			if e.Text != "" {
				fmt.Fprintf(&b, ": %s", e.Text)
			}
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// TasksCreateCmd creates a task as the operator.
type TasksCreateCmd struct {
	NodeFlags
	Title       string   `required:"" help:"A short one-line name."`
	Description string   `required:"" help:"What to do."`
	Done        string   `required:"" help:"The definition of done."`
	Tag         []string `required:"" help:"A tag of the agents that may run it. Repeat for more."`
	How         string   `help:"The skill, tool or template it is done with."`
	Parent      string   `help:"The task this one is created from."`
	Tool        []string `help:"A tool it uses. Repeat for more."`
	Script      []string `help:"A script it runs. Repeat for more."`
	Secret      string   `help:"The name of a secret the run needs, never its value."`
}

// Run implements tasks create.
func (c TasksCreateCmd) Run() error { return c.run(os.Stdout) }

func (c TasksCreateCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	t, err := cl.CreateTask(ctx, local.NewTaskJSON{
		Title: c.Title, Description: c.Description, Done: c.Done, Tags: c.Tag, How: c.How, Parent: c.Parent,
		Attachments: task.Attachments{Tools: c.Tool, Scripts: c.Script, Secret: c.Secret},
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, t.ID)
	return err
}
