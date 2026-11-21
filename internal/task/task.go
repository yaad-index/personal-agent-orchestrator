// Package task is the task, the unit of work pao dispatches (ADR-0036): what to
// do and what done means, how, the tags that decide where it runs, what it
// carries, and who owns it. A task is a file; the store keeps tasks in folders.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Status is where a task is in its life. pao sets a new task open; the agent
// working it reports the rest (ADR-0036 point 4), and pao never sets done.
type Status string

// The statuses a task can have.
const (
	Open     Status = "open"
	Assigned Status = "assigned"
	Running  Status = "running"
	Blocked  Status = "blocked"
	Done     Status = "done"
	Failed   Status = "failed"
	Canceled Status = "canceled"
)

// Statuses are every status, in a task's usual order.
var Statuses = []Status{Open, Assigned, Running, Blocked, Done, Failed, Canceled}

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Attachments are what an agent needs to run a task. They widen nothing: a tool
// or script is still bound by the running agent's access and the gate (ADR-0036
// point 2), and the secret is a reference, never its value (ADR-0036 point 3).
type Attachments struct {
	// Tools names tools the task uses.
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Scripts names scripts the task runs.
	Scripts []string `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	// Secret names the secret the run needs, resolved from the orchestrator
	// config at run. The task holds only this name.
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// Task is one task file.
type Task struct {
	// ID names the task. pao gives it at creation.
	ID string `yaml:"id" json:"id"`
	// Title is a short name for the task.
	Title string `yaml:"title" json:"title"`
	// Description is what to do.
	Description string `yaml:"description" json:"description"`
	// Done is the definition of done: what the agent reports as met.
	Done string `yaml:"done" json:"done"`
	// How names the skill, tool or template the task is done with. Its format
	// is open (ADR-0036 point 2).
	How string `yaml:"how,omitempty" json:"how,omitempty"`
	// Tags decide which agents may run it (ADR-0036 point 3).
	Tags []string `yaml:"tags" json:"tags"`
	// Owner is the subject the task belongs to, as grants name it: "operator"
	// or "agent:<name>".
	Owner string `yaml:"owner" json:"owner"`
	// Parent is the task this one was created from, if any (ADR-0036 point 5).
	Parent string `yaml:"parent,omitempty" json:"parent,omitempty"`
	// Attachments are what an agent needs to run it.
	Attachments Attachments `yaml:"attachments,omitempty" json:"attachments,omitzero"`
	// Status is where it is in its life.
	Status Status `yaml:"status" json:"status"`
	// Assignee is the agent working it, once dispatched.
	Assignee string `yaml:"assignee,omitempty" json:"assignee,omitempty"`
	// OneOff says the assignee is a temporary agent pao spawned for this task,
	// which is killed when the task is over (ADR-0036 point 1, ADR-0006).
	OneOff bool `yaml:"one_off,omitempty" json:"one_off,omitempty"`
	// Removed says that one-off agent has been killed.
	Removed bool `yaml:"removed,omitempty" json:"removed,omitempty"`
	// Created and Updated are when it was made and last changed.
	Created time.Time `yaml:"created" json:"created"`
	Updated time.Time `yaml:"updated" json:"updated"`
	// Log is what was reported on the task, oldest first: the running agent's
	// comments, status changes and log lines, and a cancel (ADR-0036 point 4).
	Log []LogEntry `yaml:"log,omitempty" json:"log,omitempty"`
	// Dropped counts the oldest entries let go to keep the log at MaxLog.
	Dropped int `yaml:"dropped,omitempty" json:"dropped,omitempty"`
}

// The kinds of log entry.
const (
	EntryComment = "comment"
	EntryStatus  = "status"
	EntryLog     = "log"
)

// LogEntry is one thing reported on a task.
type LogEntry struct {
	At time.Time `yaml:"at" json:"at"`
	// By is who reported it, as grants name a subject.
	By   string `yaml:"by" json:"by"`
	Kind string `yaml:"kind" json:"kind"`
	// Status is the status set, on a status entry.
	Status Status `yaml:"status,omitempty" json:"status,omitempty"`
	Text   string `yaml:"text,omitempty" json:"text,omitempty"`
}

// The bounds on a task's log.
const (
	MaxLog   = 500
	MaxEntry = 4 << 10
)

// Terminal reports whether a task in status s is over: it takes no more reports
// and is kept in the archive.
func (s Status) Terminal() bool { return s == Done || s == Failed || s == Canceled }

// add appends e to the log, letting the oldest entries go past MaxLog.
func (t *Task) add(e LogEntry) {
	t.Log = append(t.Log, e)
	if over := len(t.Log) - MaxLog; over > 0 {
		t.Log = append([]LogEntry(nil), t.Log[over:]...)
		t.Dropped += over
	}
	t.Updated = e.At
}

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	idPattern   = regexp.MustCompile(`^t-[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)
)

// The bounds on a task's text, so one file stays small.
const (
	MaxTitle = 200
	MaxText  = 16 << 10
	MaxItems = 32
)

// NewID returns a fresh task id: "t-", the creation time in UTC, and a random
// suffix.
func NewID(at time.Time) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "t-" + at.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// ValidID reports whether id has the shape NewID gives, so an id from a caller
// can never name a path outside the store.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Validate reports whether the task is complete and well formed.
func (t Task) Validate() error {
	if !ValidID(t.ID) {
		return fmt.Errorf("id %q is not a task id", t.ID)
	}
	if strings.TrimSpace(t.Title) == "" || len(t.Title) > MaxTitle || strings.ContainsAny(t.Title, "\n\r") {
		return fmt.Errorf("title must be one line of 1 to %d bytes", MaxTitle)
	}
	if strings.TrimSpace(t.Description) == "" || len(t.Description) > MaxText {
		return fmt.Errorf("description must be 1 to %d bytes", MaxText)
	}
	if strings.TrimSpace(t.Done) == "" || len(t.Done) > MaxText {
		return fmt.Errorf("done, the definition of done, must be 1 to %d bytes", MaxText)
	}
	if len(t.How) > MaxText {
		return fmt.Errorf("how must be at most %d bytes", MaxText)
	}
	if len(t.Tags) == 0 {
		return errors.New("at least one tag is required: tags decide where a task runs")
	}
	if err := names("tag", t.Tags); err != nil {
		return err
	}
	if t.Owner == "" {
		return errors.New("owner is required")
	}
	if t.Parent != "" && !ValidID(t.Parent) {
		return fmt.Errorf("parent %q is not a task id", t.Parent)
	}
	if err := items("tool", t.Attachments.Tools); err != nil {
		return err
	}
	if err := items("script", t.Attachments.Scripts); err != nil {
		return err
	}
	if s := t.Attachments.Secret; s != "" && !namePattern.MatchString(s) {
		return fmt.Errorf("secret %q must be a name: lowercase letters, digits, '-' or '_'", s)
	}
	if !t.Status.Valid() {
		return fmt.Errorf("status %q is not a task status", t.Status)
	}
	if t.Assignee != "" && !namePattern.MatchString(t.Assignee) {
		return fmt.Errorf("assignee %q is not an agent name", t.Assignee)
	}
	if len(t.Log) > MaxLog {
		return fmt.Errorf("at most %d log entries", MaxLog)
	}
	for i, e := range t.Log {
		switch {
		case e.Kind != EntryComment && e.Kind != EntryStatus && e.Kind != EntryLog:
			return fmt.Errorf("log[%d]: kind %q is not comment, status or log", i, e.Kind)
		case e.Kind == EntryStatus && !e.Status.Valid():
			return fmt.Errorf("log[%d]: status %q is not a task status", i, e.Status)
		case e.Kind != EntryStatus && e.Status != "":
			return fmt.Errorf("log[%d]: only a status entry sets a status", i)
		case len(e.Text) > MaxEntry:
			return fmt.Errorf("log[%d]: text must be at most %d bytes", i, MaxEntry)
		case e.By == "":
			return fmt.Errorf("log[%d]: by is required", i)
		}
	}
	return nil
}

func names(what string, vs []string) error {
	if len(vs) > MaxItems {
		return fmt.Errorf("at most %d %ss", MaxItems, what)
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if !namePattern.MatchString(v) {
			return fmt.Errorf("%s %q must be lowercase letters, digits, '-' or '_'", what, v)
		}
		if seen[v] {
			return fmt.Errorf("%s %q is given twice", what, v)
		}
		seen[v] = true
	}
	return nil
}

func items(what string, vs []string) error {
	if len(vs) > MaxItems {
		return fmt.Errorf("at most %d %ss", MaxItems, what)
	}
	for _, v := range vs {
		if strings.TrimSpace(v) == "" || len(v) > 256 || strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("%s %q must be one non-empty line of at most 256 bytes", what, v)
		}
	}
	return nil
}
