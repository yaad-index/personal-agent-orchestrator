package task

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Recipe is a recipe a one-off agent could be spawned from for a task.
type Recipe struct {
	Name    string
	Tags    []string
	Account string
	Budget  core.BudgetRule
}

// OneOffs spawns and kills the temporary agents pao starts for tasks
// (ADR-0036 point 1). Both act for the task's owner as requester, so the
// access matrix and the gate judge them as they would the owner's own spawn or
// kill (ADR-0006 points 2 and 3).
type OneOffs interface {
	Recipes() []Recipe
	// Spawn starts an agent from recipe and returns its name, and where its
	// backend keeps its transcript, once it is ready. A secret, when named, is
	// materialized for that agent's run alone.
	Spawn(ctx context.Context, owner access.Subject, recipe, secret string) (name, transcript string, err error)
	// Kill tears agent down, harvesting first, and returns the harvest's id.
	// gone says there was no such agent to kill.
	Kill(ctx context.Context, owner access.Subject, agent string) (harvest string, gone bool, err error)
	// Forget removes a killed agent's transcript and confirms it is gone. A
	// transcript it cannot find, or cannot remove, is an error.
	Forget(agent, transcript string) error
}

// Liveness says whether an agent has gone quiet (ADR-0007 point 5).
type Liveness interface {
	Stalled(agent string) bool
}

// retryRemoval is how long after a failed kill it is tried again, and
// retrySpawn after a failed spawn, so a refused one is not asked about on
// every pass.
const (
	retryRemoval = 5 * time.Minute
	retrySpawn   = 5 * time.Minute
)

// pickRecipe chooses the recipe to spawn a one-off from for t, or says why
// there is none.
func (d *Dispatcher) pickRecipe(t Task) (string, string) {
	for _, tool := range t.Attachments.Tools {
		if d.Tools != nil && d.Tools.Proxied(tool) {
			// A new agent holds no grant by name, so no proxied tool is in its
			// reach: such a task can only go to an agent that already exists.
			return "", fmt.Sprintf("a new agent could not reach the attached tool %s", tool)
		}
	}
	var carriers []Recipe
	for _, r := range d.OneOffs.Recipes() {
		if carries(r.Tags, t.Tags) {
			carriers = append(carriers, r)
		}
	}
	if len(carriers) == 0 {
		return "", "no recipe carries every tag"
	}
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].Name < carriers[j].Name })
	for _, r := range carriers {
		if d.Budget != nil && d.Budget.Headroom(r.Account, r.Budget) {
			return r.Name, ""
		}
	}
	return "", "no recipe that carries every tag has budget headroom"
}

// begin marks id as having work in flight; it reports false if some already is.
func (d *Dispatcher) begin(set map[string]bool, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if set[id] {
		return false
	}
	set[id] = true
	return true
}

func (d *Dispatcher) end(set map[string]bool, id string) {
	d.mu.Lock()
	delete(set, id)
	d.mu.Unlock()
}

func (d *Dispatcher) inFlight(set map[string]bool, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return set[id]
}

// spawnFor starts a one-off agent from recipe for the task and assigns the task
// to it. A spawn takes as long as the agent takes to be ready, and as long as
// the gate takes to answer, so it runs beside the pass, never in it.
func (d *Dispatcher) spawnFor(ctx context.Context, id, recipe, secret string, owner access.Subject) {
	defer func() {
		d.end(d.spawning, id)
		d.Kick()
	}()
	name, transcript, err := d.OneOffs.Spawn(ctx, owner, recipe, secret)
	if err != nil {
		d.mu.Lock()
		d.spawnAt[id] = d.Svc.now().Add(retrySpawn)
		d.mu.Unlock()
		d.wait(id, fmt.Sprintf("a one-off from recipe %s could not be started: %v", recipe, err))
		return
	}
	d.mu.Lock()
	delete(d.spawnAt, id)
	d.mu.Unlock()
	d.Svc.mu.Lock()
	t, _, err := d.Svc.Store.Get(id)
	if err != nil || t.Status != Open || t.Assignee != "" {
		d.Svc.mu.Unlock()
		// The task went elsewhere, or away, while the agent started.
		d.kill(ctx, owner, name)
		return
	}
	t.Assignee, t.Status, t.OneOff = name, Assigned, true
	if secret != "" {
		t.Transcript = transcript
	}
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryStatus, Status: Assigned, Text: fmt.Sprintf("assigned to %s, a one-off from recipe %s", name, recipe)})
	err = d.Svc.Store.Put(FolderOpen, t)
	d.Svc.mu.Unlock()
	if err != nil {
		slog.Warn("dispatch: could not assign a task to its one-off", "task", id, "agent", name, "error", err)
		d.kill(ctx, owner, name)
		return
	}
	text := ""
	if env, hosts, ok := d.injected(secret); ok {
		text = BriefInjected(t, env, hosts)
	} else {
		path := ""
		if secret != "" && d.SecretPath != nil {
			if p, err := d.SecretPath(name, secret); err == nil {
				path = p
			}
		}
		text = BriefWith(t, path)
	}
	if err := d.Fleet.Deliver(ctx, name, text); err != nil {
		d.unassign(id, name, err)
		d.kill(ctx, owner, name)
	}
}

func (d *Dispatcher) injected(secret string) (string, []string, bool) {
	if secret == "" || d.Injected == nil {
		return "", nil, false
	}
	return d.Injected(secret)
}

// kill tears down a one-off that never took its task, logging a failure.
func (d *Dispatcher) kill(ctx context.Context, owner access.Subject, name string) {
	if _, _, err := d.OneOffs.Kill(ctx, owner, name); err != nil {
		slog.Warn("dispatch: could not kill a one-off that took no task", "agent", name, "error", err)
	}
}

// remove kills the one-off agent of a task that is over, harvesting first, and
// notes it in the task's log.
func (d *Dispatcher) remove(ctx context.Context, id string) {
	defer d.end(d.removing, id)
	t, _, err := d.Svc.Store.Get(id)
	if err != nil || !t.Status.Terminal() || !t.OneOff || t.Removed || t.Assignee == "" {
		return
	}
	owner, err := access.ParseSubject(t.Owner)
	if err != nil {
		return
	}
	harvest, gone, err := d.OneOffs.Kill(ctx, owner, t.Assignee)
	if err != nil {
		d.mu.Lock()
		d.retryAt[id] = d.Svc.now().Add(retryRemoval)
		d.mu.Unlock()
		d.note(id, fmt.Sprintf("one-off %s could not be removed: %v", t.Assignee, err))
		return
	}
	text := fmt.Sprintf("one-off %s removed; its outputs are in harvest %s", t.Assignee, harvest)
	if gone {
		text = fmt.Sprintf("one-off %s was already gone", t.Assignee)
	}
	if t.Attachments.Secret != "" {
		// The backend's transcript of a run with a secret holds what its tools
		// returned, the value among them: it goes with the agent, after the
		// harvest (ADR-0037 point 3).
		if ferr := d.OneOffs.Forget(t.Assignee, t.Transcript); ferr != nil {
			text += fmt.Sprintf("; its transcript could NOT be removed: %v", ferr)
			if d.Warn != nil {
				d.Warn(fmt.Sprintf("task %s: the transcript of one-off %s, which ran with a secret, could not be removed: %v. The secret's value may still be in it.", id, t.Assignee, ferr))
			}
		} else {
			text += "; its transcript was removed"
		}
	}
	d.Svc.mu.Lock()
	defer d.Svc.mu.Unlock()
	t, folder, err := d.Svc.Store.Get(id)
	if err != nil {
		return
	}
	t.Removed = true
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryLog, Text: text})
	if err := d.Svc.Store.Put(folder, t); err != nil {
		slog.Warn("dispatch: could not note a removed one-off", "task", id, "error", err)
	}
}

// watch notes in the task's log when its assignee goes quiet, once each time.
func (d *Dispatcher) watch(t Task) {
	if d.Liveness == nil {
		return
	}
	stalled := d.Liveness.Stalled(t.Assignee)
	text := fmt.Sprintf("assignee %s has gone quiet", t.Assignee)
	if !stalled {
		text = fmt.Sprintf("assignee %s is active again", t.Assignee)
	}
	last := ""
	for i := len(t.Log) - 1; i >= 0; i-- {
		if e := t.Log[i]; e.By == Pao && strings.HasPrefix(e.Text, "assignee ") {
			last = e.Text
			break
		}
	}
	// Only a change is noted, and "active again" only after "gone quiet".
	if text == last || (!stalled && last == "") {
		return
	}
	d.note(t.ID, text)
}

// note adds a pao log line to the task, whatever its status.
func (d *Dispatcher) note(id, text string) {
	d.Svc.mu.Lock()
	defer d.Svc.mu.Unlock()
	t, folder, err := d.Svc.Store.Get(id)
	if err != nil {
		return
	}
	if n := len(t.Log); n > 0 && t.Log[n-1].By == Pao && t.Log[n-1].Text == text {
		return
	}
	t.add(LogEntry{At: d.Svc.now(), By: Pao, Kind: EntryLog, Text: text})
	if err := d.Svc.Store.Put(folder, t); err != nil {
		slog.Warn("dispatch: could not note on a task", "task", id, "error", err)
	}
}
