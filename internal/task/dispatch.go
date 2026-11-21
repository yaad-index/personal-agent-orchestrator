package task

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Candidate is an agent a task could be dispatched to.
type Candidate struct {
	Name    string
	Tags    []string
	Account string
	Budget  core.BudgetRule
	// Running says its session is up. A stopped configured agent is still a
	// candidate: delivering to it starts it, as routing does.
	Running bool
}

// Fleet is the node's agents as dispatch sees them.
type Fleet interface {
	// Candidates are the agents work may be dispatched to on this node. The
	// governor is never one: it runs no work.
	Candidates() []Candidate
	// Deliver hands text to the agent as a message from pao.
	Deliver(ctx context.Context, agent, text string) error
}

// Headroom answers whether an agent on account, judged by its rule, may be
// given work (ADR-0035).
type Headroom interface {
	Headroom(account string, rule core.BudgetRule) bool
}

// Dispatcher hands open tasks to agents (ADR-0036 point 3): an agent that
// carries every one of a task's tags, that the task's owner may send work to,
// that holds no other task, and whose account has headroom. With none, the
// task waits.
type Dispatcher struct {
	Svc    *Service
	Fleet  Fleet
	Budget Headroom
	// OneOffs, when set, spawns an agent for a task no existing agent can run,
	// and kills it when the task is over.
	OneOffs OneOffs
	// Liveness, when set, says whether an assignee has gone quiet.
	Liveness Liveness

	kick chan struct{}

	mu       sync.Mutex
	spawning map[string]bool
	removing map[string]bool
	retryAt  map[string]time.Time
}

// Pao is the name pao's own log entries are made under.
const Pao = "pao"

// Kick asks for a dispatch pass soon, as after a task is created or finished.
func (d *Dispatcher) Kick() {
	if d.kick == nil {
		return
	}
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run dispatches every interval, and when kicked, until ctx ends.
func (d *Dispatcher) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		d.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.kick:
		}
	}
}

// NewDispatcher returns a dispatcher whose Kick wakes its Run.
func NewDispatcher(svc *Service, fleet Fleet, budget Headroom) *Dispatcher {
	return &Dispatcher{Svc: svc, Fleet: fleet, Budget: budget, kick: make(chan struct{}, 1),
		spawning: map[string]bool{}, removing: map[string]bool{}, retryAt: map[string]time.Time{}}
}

// Pass dispatches the open tasks once, the oldest first.
func (d *Dispatcher) Pass(ctx context.Context) {
	all, err := d.Svc.Store.List()
	if err != nil {
		slog.Warn("dispatch: some task files could not be read", "error", err)
	}
	busy := map[string]bool{}
	var open []Task
	for _, e := range all {
		t := e.Task
		switch {
		case t.Assignee != "" && !t.Status.Terminal():
			busy[t.Assignee] = true
			d.watch(t)
		case t.Assignee != "" && t.OneOff && !t.Removed:
			d.startRemoval(ctx, t.ID)
		case e.Folder == FolderOpen && t.Status == Open && t.Assignee == "":
			open = append(open, t)
		}
	}
	sort.Slice(open, func(i, j int) bool {
		if !open[i].Created.Equal(open[j].Created) {
			return open[i].Created.Before(open[j].Created)
		}
		return open[i].ID < open[j].ID
	})
	candidates := d.Fleet.Candidates()
	for _, t := range open {
		if ctx.Err() != nil {
			return
		}
		if d.inFlight(d.spawning, t.ID) {
			continue
		}
		agent, why := d.pick(t, candidates, busy)
		if agent == "" && d.OneOffs != nil {
			recipe, rwhy := d.pickRecipe(t)
			if recipe != "" {
				if owner, err := access.ParseSubject(t.Owner); err == nil && d.begin(d.spawning, t.ID) {
					go d.spawnFor(ctx, t.ID, recipe, owner)
				}
				continue
			}
			why += "; " + rwhy
		}
		if agent == "" {
			d.wait(t.ID, why)
			continue
		}
		if d.assign(ctx, t.ID, agent) {
			busy[agent] = true
		}
	}
}

// startRemoval kills a finished task's one-off beside the pass, unless a kill
// is already under way or failed too recently.
func (d *Dispatcher) startRemoval(ctx context.Context, id string) {
	if d.OneOffs == nil {
		return
	}
	d.mu.Lock()
	if at, ok := d.retryAt[id]; ok && d.Svc.now().Before(at) {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	if d.begin(d.removing, id) {
		go d.remove(ctx, id)
	}
}

// pick chooses the agent for t, or says why there is none.
func (d *Dispatcher) pick(t Task, candidates []Candidate, busy map[string]bool) (string, string) {
	owner, err := access.ParseSubject(t.Owner)
	if err != nil {
		return "", fmt.Sprintf("its owner %q is not a subject", t.Owner)
	}
	var free, withHeadroom []Candidate
	carriers := 0
	for _, c := range candidates {
		if !carries(c.Tags, t.Tags) || !access.Allowed(d.Svc.Auth, owner, access.Route, access.AgentResource(c.Name, c.Tags)) {
			continue
		}
		carriers++
		if busy[c.Name] {
			continue
		}
		free = append(free, c)
		if d.Budget != nil && d.Budget.Headroom(c.Account, c.Budget) {
			withHeadroom = append(withHeadroom, c)
		}
	}
	switch {
	case carriers == 0:
		return "", fmt.Sprintf("no agent its owner may reach carries every tag of %s", strings.Join(t.Tags, ", "))
	case len(free) == 0:
		return "", "every agent that could run it holds another task"
	case len(withHeadroom) == 0:
		return "", "no agent that could run it has budget headroom"
	}
	sort.SliceStable(withHeadroom, func(i, j int) bool {
		if withHeadroom[i].Running != withHeadroom[j].Running {
			return withHeadroom[i].Running
		}
		return withHeadroom[i].Name < withHeadroom[j].Name
	})
	return withHeadroom[0].Name, ""
}

// carries reports whether have holds every tag in want.
func carries(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// wait notes in the task's log why it waits, once for each new reason.
func (d *Dispatcher) wait(id, why string) {
	text := "waiting: " + why
	d.Svc.mu.Lock()
	defer d.Svc.mu.Unlock()
	t, folder, err := d.Svc.Store.Get(id)
	if err != nil || t.Status != Open || t.Assignee != "" {
		return
	}
	for i := len(t.Log) - 1; i >= 0; i-- {
		if e := t.Log[i]; e.By == Pao && strings.HasPrefix(e.Text, "waiting: ") {
			if e.Text == text {
				return
			}
			break
		}
	}
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryLog, Text: text})
	if err := d.Svc.Store.Put(folder, t); err != nil {
		slog.Warn("dispatch: could not note why a task waits", "task", id, "error", err)
	}
}

// assign gives the task to agent and tells the agent. If the agent cannot be
// told, the assignment is taken back and the task is open again.
func (d *Dispatcher) assign(ctx context.Context, id, agent string) bool {
	d.Svc.mu.Lock()
	t, _, err := d.Svc.Store.Get(id)
	if err != nil || t.Status != Open || t.Assignee != "" {
		d.Svc.mu.Unlock()
		return false
	}
	t.Assignee, t.Status = agent, Assigned
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryStatus, Status: Assigned, Text: "assigned to " + agent})
	err = d.Svc.Store.Put(FolderOpen, t)
	d.Svc.mu.Unlock()
	if err != nil {
		slog.Warn("dispatch: could not assign a task", "task", id, "agent", agent, "error", err)
		return false
	}
	if err := d.Fleet.Deliver(ctx, agent, Brief(t)); err != nil {
		slog.Warn("dispatch: could not tell the agent of its task", "task", id, "agent", agent, "error", err)
		d.unassign(id, agent, err)
		return false
	}
	return true
}

// unassign takes a task back from agent, if it is still assigned to it and the
// agent has reported nothing since.
func (d *Dispatcher) unassign(id, agent string, why error) {
	d.Svc.mu.Lock()
	defer d.Svc.mu.Unlock()
	t, _, err := d.Svc.Store.Get(id)
	if err != nil || t.Assignee != agent || t.Status != Assigned {
		return
	}
	t.Assignee, t.Status = "", Open
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryStatus, Status: Open, Text: fmt.Sprintf("%s could not be told of it: %v", agent, why)})
	if err := d.Svc.Store.Put(FolderOpen, t); err != nil {
		slog.Warn("dispatch: could not take a task back", "task", id, "error", err)
	}
}

// Brief is the message an agent is given its task in.
func Brief(t Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are assigned task %s: %s\n\n%s\n\nDone when: %s\n", t.ID, t.Title, t.Description, t.Done)
	if t.How != "" {
		fmt.Fprintf(&b, "How: %s\n", t.How)
	}
	if len(t.Attachments.Tools) > 0 {
		fmt.Fprintf(&b, "Tools: %s\n", strings.Join(t.Attachments.Tools, ", "))
	}
	if len(t.Attachments.Scripts) > 0 {
		fmt.Fprintf(&b, "Scripts: %s\n", strings.Join(t.Attachments.Scripts, ", "))
	}
	if t.Attachments.Secret != "" {
		// The name alone: a secret's value never crosses a channel (ADR-0036
		// point 3).
		fmt.Fprintf(&b, "Secret: %s (by name only)\n", t.Attachments.Secret)
	}
	if t.Parent != "" {
		fmt.Fprintf(&b, "Created from task: %s\n", t.Parent)
	}
	fmt.Fprintf(&b, "\nReport on it with the report_task tool, id %s: status running when you start, comments and log lines as you go, and done or failed when it is over. Done is your word that the definition of done is met.", t.ID)
	return b.String()
}
