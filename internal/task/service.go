package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	return t, nil
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
	if errors.Is(err, ErrNotFound) || (err == nil && !s.visible(who, t)) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if err := s.approve(ctx, who, ActionRead, map[string]string{"id": id}); err != nil {
		return Entry{}, err
	}
	return Entry{Task: t, Folder: folder}, nil
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
