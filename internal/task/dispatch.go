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
	"github.com/yaad-index/personal-agent-orchestrator/internal/runsecret"
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

// Tools says which tool names are tools pao offers on behalf of an upstream
// server, and whether an agent's own access reaches one.
type Tools interface {
	Proxied(tool string) bool
	Reaches(agent, tool string) bool
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
	// Injected, when set, says whether a secret is put into a one-off's calls
	// by the egress proxy, with the variable holding its placeholder and the
	// hosts it is filled in for, so the brief says so instead of a file.
	Injected func(secret string) (env string, hosts []string, ok bool)
	// SecretPath, when set, says where a one-off's secret is put, so its brief
	// can name the file as well as the variable: an agent with no shell cannot
	// read a variable. The path is not the secret (ADR-0036 point 3).
	SecretPath func(agent, secret string) (string, error)
	// Warn, when set, tells the operator of something pao could not do that
	// leaves a risk behind, such as a transcript of a run with a secret it
	// could not remove.
	Warn func(text string)
	// Tools, when set, says which attached tools are pao's proxied tools and
	// who reaches them. An attachment grants nothing; such a tool only narrows
	// the agents a task can go to (ADR-0036 point 2).
	Tools Tools

	kick chan struct{}

	mu       sync.Mutex
	spawning map[string]bool
	removing map[string]bool
	retryAt  map[string]time.Time
	// spawnAt is when a task whose one-off failed to start may be tried again.
	spawnAt map[string]time.Time
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
		spawning: map[string]bool{}, removing: map[string]bool{}, retryAt: map[string]time.Time{}, spawnAt: map[string]time.Time{}}
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
			if recipe != "" && d.spawnHeld(t.ID) {
				// Its last one-off failed to start: it waits out the retry,
				// so a refused spawn is not asked about on every pass.
				continue
			}
			if recipe != "" {
				if owner, err := access.ParseSubject(t.Owner); err == nil && d.begin(d.spawning, t.ID) {
					go d.spawnFor(ctx, t.ID, recipe, t.Attachments.Secret, owner)
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

// spawnHeld reports whether a task's last one-off failed to start too recently
// to try again.
func (d *Dispatcher) spawnHeld(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.spawnAt[id]
	return ok && d.Svc.now().Before(at)
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
	if t.Attachments.Secret != "" {
		// A running agent's environment cannot be made for one run alone, so a
		// task with a secret runs on a one-off started for it (ADR-0036 point 3).
		return "", "it names a secret, so it runs only on an agent started for it"
	}
	var free, withHeadroom []Candidate
	carriers := 0
	for _, c := range candidates {
		if !carries(c.Tags, t.Tags) || !access.Allowed(d.Svc.Auth, owner, access.Route, access.AgentResource(c.Name, c.Tags)) || !d.reachesTools(c.Name, t) {
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
		return "", fmt.Sprintf("no agent its owner may reach, and that reaches its tools, carries every tag of %s", strings.Join(t.Tags, ", "))
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

// reachesTools reports whether agent's own access reaches every attached tool
// that is one of pao's proxied tools.
func (d *Dispatcher) reachesTools(agent string, t Task) bool {
	if d.Tools == nil {
		return true
	}
	for _, tool := range t.Attachments.Tools {
		if d.Tools.Proxied(tool) && !d.Tools.Reaches(agent, tool) {
			return false
		}
	}
	return true
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
func Brief(t Task) string { return BriefWith(t, "") }

// BriefWith is Brief naming secretPath, the file the task's secret is in for
// this run, beside the variable that holds it.
func BriefWith(t Task, secretPath string) string {
	return brief(t, func(sec string) string {
		// The name and where to find it: a secret's value never crosses a
		// channel (ADR-0036 point 3).
		where := fmt.Sprintf("the file whose path is in $%s", runsecret.EnvName(sec))
		if secretPath != "" {
			where = fmt.Sprintf("the file %s (its path is also in $%s)", secretPath, runsecret.EnvName(sec))
		}
		return fmt.Sprintf("Secret: %s. Its value is in %s, for this run only. Read it with a file-reading tool. Never print, log, commit or copy the value anywhere.\n", sec, where)
	})
}

// BriefInjected is Brief for a task whose secret is put into the agent's calls
// by the egress proxy (ADR-0049): the agent is told the variable holding the
// placeholder and the hosts it is filled in for, and never sees the value.
func BriefInjected(t Task, env string, hosts []string) string {
	return brief(t, func(sec string) string {
		return fmt.Sprintf("Secret: %s. Use $%s as the credential in your requests to %s: it holds a placeholder that pao replaces with the real value on the way out, and only for those hosts. You never see the value, and the placeholder works nowhere else.\n", sec, env, strings.Join(hosts, ", "))
	})
}

func brief(t Task, secretLine func(sec string) string) string {
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
	if sec := t.Attachments.Secret; sec != "" {
		b.WriteString(secretLine(sec))
	}
	if t.Parent != "" {
		fmt.Fprintf(&b, "Created from task: %s\n", t.Parent)
	}
	fmt.Fprintf(&b, "\nReport on it with the report_task tool, id %s: status running when you start, comments and log lines as you go, and done or failed when it is over. Done is your word that the definition of done is met.", t.ID)
	return b.String()
}
