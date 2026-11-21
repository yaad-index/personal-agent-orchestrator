package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

var (
	// ErrBadRequest is a task that is malformed.
	ErrBadRequest = errors.New("bad task")
	// ErrDenied is a request the approval gate refused.
	ErrDenied = errors.New("request denied")
)

// The names the gate judges task actions by.
const (
	ActionCreate = "pao.create_task"
	ActionList   = "pao.list_tasks"
	ActionRead   = "pao.read_task"
)

// Gate is the approval gate.
type Gate interface {
	Decide(ctx context.Context, req gate.Request) gate.Decision
}

// Service is the task operations every surface calls, under the same checks:
// the access matrix decides who may create a task for which tags and who may
// see one, and the gate judges an agent's request (ADR-0016 point 4).
type Service struct {
	Store *Store
	Auth  access.Authorizer
	Gate  Gate
	Now   func() time.Time
	// Changed is called after a task is created, reported on or canceled, so
	// dispatch can look again. It may be nil.
	Changed func()
	// Tell hands text to an agent as a message from pao: a canceled task's
	// assignee is told to stop. It may be nil.
	Tell func(ctx context.Context, agent, text string) error
	// HasSecret says whether the orchestrator config holds a secret of that
	// name. A task naming one it does not is refused. Nil holds none.
	HasSecret func(name string) bool
	// Proxied says whether a tool is one of pao's proxied tools. It may be nil.
	Proxied func(tool string) bool

	// mu serialises read-modify-write of one task file.
	mu sync.Mutex
}

// New is what a caller asks to create: everything of a task but what pao sets.
type New struct {
	Title       string
	Description string
	Done        string
	How         string
	Tags        []string
	Parent      string
	Attachments Attachments
}

// Create makes an open task owned by who. Each of its tags must be one who may
// send work to, so a task never reaches an agent its creator could not
// (ADR-0036 point 2); a tag out of reach is refused as not found, as an agent
// out of reach is. A parent must be a task who may see.
func (s *Service) Create(ctx context.Context, who access.Subject, n New) (Task, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	at := now().UTC().Truncate(time.Second)
	id, err := NewID(at)
	if err != nil {
		return Task{}, err
	}
	t := Task{
		ID: id, Title: n.Title, Description: n.Description, Done: n.Done, How: n.How,
		Tags: n.Tags, Owner: who.String(), Parent: n.Parent, Attachments: n.Attachments,
		Status: Open, Created: at, Updated: at,
	}
	if err := t.Validate(); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if sec := t.Attachments.Secret; sec != "" && (s.HasSecret == nil || !s.HasSecret(sec)) {
		return Task{}, fmt.Errorf("%w: no secret named %q is configured", ErrBadRequest, sec)
	}
	if t.Attachments.Secret != "" && s.Proxied != nil {
		for _, tool := range t.Attachments.Tools {
			if s.Proxied(tool) {
				// A secret runs only on an agent started for the task, and a
				// new agent reaches no proxied tool: such a task could never run.
				return Task{}, fmt.Errorf("%w: a task with a secret runs only on an agent started for it, which cannot reach the proxied tool %s; split it into two tasks", ErrBadRequest, tool)
			}
		}
	}
	for _, tag := range t.Tags {
		// By tag alone: a grant of one agent's name says nothing of the work
		// that may be sent to every agent carrying a tag.
		if !access.Allowed(s.Auth, who, access.Route, access.Resource{Kind: access.ResourceAgent, Tags: []string{tag}}) {
			return Task{}, ErrNotFound
		}
	}
	if t.Parent != "" {
		p, _, err := s.Store.Get(t.Parent)
		if err != nil || !s.visible(who, p) {
			return Task{}, fmt.Errorf("%w: parent %s", ErrNotFound, t.Parent)
		}
	}
	if err := s.approve(ctx, who, ActionCreate, map[string]string{"title": t.Title, "tags": strings.Join(t.Tags, ",")}); err != nil {
		return Task{}, err
	}
	if err := s.Store.Put(FolderOpen, t); err != nil {
		return Task{}, err
	}
	s.changed()
	return t, nil
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// List returns the tasks who may see, the newest first, with their folders.
func (s *Service) List(ctx context.Context, who access.Subject) ([]Entry, error) {
	all, err := s.Store.List()
	var out []Entry
	for _, e := range all {
		if s.visible(who, e.Task) {
			out = append(out, e)
		}
	}
	if err != nil && who.Kind != access.KindOperator {
		// A broken file is the operator's to see and mend; to anyone else the
		// tasks that read are the list.
		err = nil
	}
	if aerr := s.approve(ctx, who, ActionList, nil); aerr != nil {
		return nil, aerr
	}
	return out, err
}

// Get returns the task of that id if who may see it. One who may not is told
// it is not found, as if it did not exist.
func (s *Service) Get(ctx context.Context, who access.Subject, id string) (Entry, error) {
	t, folder, err := s.Store.Get(id)
	if err != nil {
		return Entry{}, s.storeError(who, err)
	}
	if !s.visible(who, t) {
		return Entry{}, ErrNotFound
	}
	if err := s.approve(ctx, who, ActionRead, map[string]string{"id": id}); err != nil {
		return Entry{}, err
	}
	return Entry{Task: t, Folder: folder}, nil
}

// storeError is what who is told when a task could not be read. A file that is
// broken is the operator's to see and mend; to anyone else it is not found, as a
// task they may not see is, since whose it is cannot be read either.
func (s *Service) storeError(who access.Subject, err error) error {
	if errors.Is(err, ErrNotFound) || who.Kind != access.KindOperator {
		return ErrNotFound
	}
	return err
}

// visible reports whether who may see t: its owner may, as may whoever may see
// an agent carrying one of its tags, since such an agent is where it runs.
func (s *Service) visible(who access.Subject, t Task) bool {
	if who.String() != "" && who.String() == t.Owner {
		return true
	}
	for _, tag := range t.Tags {
		if access.Allowed(s.Auth, who, access.List, access.Resource{Kind: access.ResourceAgent, Tags: []string{tag}}) {
			return true
		}
	}
	return false
}

// approve asks the gate about an agent's request. The operator is not asked:
// the gate judges what agents do (ADR-0004).
func (s *Service) approve(ctx context.Context, who access.Subject, action string, detail any) error {
	if who.Kind == access.KindOperator {
		return nil
	}
	if who.Kind != access.KindAgent {
		return ErrNotFound
	}
	if s.Gate == nil {
		return fmt.Errorf("%w: no approval gate is configured", ErrDenied)
	}
	input, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDenied, err)
	}
	d := s.Gate.Decide(ctx, gate.Request{Agent: who.Name, Tool: action, Input: input})
	if !d.Allow {
		return fmt.Errorf("%w: %s", ErrDenied, d.Reason)
	}
	return nil
}

// ErrNotAssignee is a report from anyone but the agent working the task.
var ErrNotAssignee = errors.New("only the agent working a task reports on it")

// The names the gate judges task changes by.
const (
	ActionReport = "pao.report_task"
	ActionCancel = "pao.cancel_task"
)

// AgentStatuses are the statuses the agent working a task may report: pao sets
// open and assigned, and the owner cancels.
var AgentStatuses = []Status{Running, Blocked, Done, Failed}

// Report is what the agent working a task reports: a status, a comment, a log
// line, or any of them together.
type Report struct {
	Status  Status
	Comment string
	Log     string
}

// Report records what the agent working a task reports on it (ADR-0036 point
// 4). Only its assignee may report, and only while it is not over. A status of
// done or failed is the agent's own word that the task is over; pao takes it as
// reported and does not judge it, and the task goes to the archive.
func (s *Service) Report(ctx context.Context, who access.Subject, id string, r Report) (Task, error) {
	if r.Status == "" && strings.TrimSpace(r.Comment) == "" && strings.TrimSpace(r.Log) == "" {
		return Task{}, fmt.Errorf("%w: a report carries a status, a comment or a log line", ErrBadRequest)
	}
	if r.Status != "" && !slices.Contains(AgentStatuses, r.Status) {
		return Task{}, fmt.Errorf("%w: an agent reports a status of running, blocked, done or failed, not %q", ErrBadRequest, r.Status)
	}
	for _, text := range []string{r.Comment, r.Log} {
		if len(text) > MaxEntry {
			return Task{}, fmt.Errorf("%w: a comment or log line is at most %d bytes", ErrBadRequest, MaxEntry)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, err := s.Store.Get(id)
	if err != nil {
		return Task{}, s.storeError(who, err)
	}
	if !s.visible(who, t) && !assigned(who, t) {
		return Task{}, ErrNotFound
	}
	if !assigned(who, t) {
		return Task{}, ErrNotAssignee
	}
	if t.Status.Terminal() {
		return Task{}, fmt.Errorf("%w: task %s is %s and takes no more reports", ErrBadRequest, t.ID, t.Status)
	}
	if err := s.approve(ctx, who, ActionReport, map[string]string{"id": id, "status": string(r.Status)}); err != nil {
		return Task{}, err
	}
	at := s.now()
	by := who.String()
	if r.Comment != "" {
		t.add(LogEntry{At: at, By: by, Kind: EntryComment, Text: r.Comment})
	}
	if r.Log != "" {
		t.add(LogEntry{At: at, By: by, Kind: EntryLog, Text: r.Log})
	}
	if r.Status != "" && r.Status != t.Status {
		t.Status = r.Status
		t.add(LogEntry{At: at, By: by, Kind: EntryStatus, Status: r.Status})
	}
	folder := FolderOpen
	if t.Status.Terminal() {
		folder = FolderArchive
	}
	if err := s.Store.Put(folder, t); err != nil {
		return Task{}, err
	}
	s.changed()
	return t, nil
}

// Cancel ends a task that is not over: its owner may, and the operator may for
// a task it can see. It goes to the archive, with the reason in its log.
func (s *Service) Cancel(ctx context.Context, who access.Subject, id, reason string) (Task, error) {
	t, err := s.cancel(ctx, who, id, reason)
	if err != nil {
		return Task{}, err
	}
	if t.Assignee != "" && s.Tell != nil {
		text := fmt.Sprintf("Task %s (%s) was canceled by %s. Stop working on it.", t.ID, t.Title, who.String())
		if reason != "" {
			text += " Reason: " + reason
		}
		if err := s.Tell(ctx, t.Assignee, text); err != nil {
			slog.Warn("could not tell an assignee its task was canceled", "task", t.ID, "agent", t.Assignee, "error", err)
		}
	}
	return t, nil
}

func (s *Service) cancel(ctx context.Context, who access.Subject, id, reason string) (Task, error) {
	if len(reason) > MaxEntry {
		return Task{}, fmt.Errorf("%w: a reason is at most %d bytes", ErrBadRequest, MaxEntry)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _, err := s.Store.Get(id)
	if err != nil {
		return Task{}, s.storeError(who, err)
	}
	if !s.visible(who, t) {
		return Task{}, ErrNotFound
	}
	if who.String() != t.Owner && who.Kind != access.KindOperator {
		return Task{}, fmt.Errorf("%w: only the task's owner or the operator cancels it", ErrDenied)
	}
	if t.Status.Terminal() {
		return Task{}, fmt.Errorf("%w: task %s is already %s", ErrBadRequest, t.ID, t.Status)
	}
	if err := s.approve(ctx, who, ActionCancel, map[string]string{"id": id}); err != nil {
		return Task{}, err
	}
	t.Status = Canceled
	t.add(LogEntry{At: s.now(), By: who.String(), Kind: EntryStatus, Status: Canceled, Text: reason})
	if err := s.Store.Put(FolderArchive, t); err != nil {
		return Task{}, err
	}
	s.changed()
	return t, nil
}

// assigned reports whether who is the agent working t.
func assigned(who access.Subject, t Task) bool {
	return who.Kind == access.KindAgent && t.Assignee != "" && who.Name == t.Assignee
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}
