// Package temp runs the lifecycle of temporary agents (ADR-0006): spawning one
// from a recipe into an isolated workdir on a named channel, and killing one,
// which always harvests its outputs before anything is destroyed. A temporary
// agent is otherwise an ordinary registered agent, so everything else (routing,
// the mirror, the message model) reaches it unchanged.
//
// The operator may do anything here. An agent asking is a different subject:
// it passes the access seam and then the approval gate, and anything that goes
// wrong is a refusal.
package temp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

var (
	// ErrNotFound is returned for an agent that is unknown, is not a temporary
	// agent, or that the requester may not act on. They are indistinguishable.
	ErrNotFound = errors.New("no such temporary agent")
	// ErrDenied is returned when the approval gate refused an agent's request.
	ErrDenied = errors.New("request denied")
	// ErrBadRequest is returned for a request that is malformed: a bad name, or
	// a channel that is missing or unknown.
	ErrBadRequest = errors.New("bad request")
	// ErrNoRecipe is returned for a recipe that is not installed.
	ErrNoRecipe = errors.New("no such recipe")
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Plane is the control plane's side of a kill (ADR-0007).
type Plane interface {
	Kill(ctx context.Context, p core.Profile) error
}

// Starter brings an agent up through the control plane and the router's launch
// lock, and reports whether it launched it.
type Starter interface {
	Ensure(ctx context.Context, p core.Profile) (bool, error)
}

// Channels is the communication layer's binding of an agent to a channel.
type Channels interface {
	HasChannel(name string) bool
	Channels() []string
	Bind(agent, channel string) error
	Unbind(agent string)
}

// Gate is the approval gate.
type Gate interface {
	Decide(ctx context.Context, req gate.Request) gate.Decision
}

// Follower stops following an agent's transcript.
type Follower interface {
	Stop(name string)
}

// Surface reports text to whoever asked for an action: the operator or an
// agent.
type Surface func(ctx context.Context, who access.Subject, text string)

// Manager spawns and kills temporary agents. All fields but Auth, Follow,
// Surface, Locate and Now are required.
type Manager struct {
	Registry *registry.Registry
	Plane    Plane
	Starter  Starter
	Channels Channels
	Workdirs *workdir.Provisioner
	Harvests *harvest.Store
	// Recipes returns the installed recipes. It is called on every spawn, so a
	// set pulled while the node runs is available at once.
	Recipes func() (*recipe.Set, error)
	// Locate says where a backend writes the transcripts of sessions in a
	// workdir; recipes that leave transcript.path empty use it.
	Locate recipe.Locator
	// Auth is the access seam; nil means operator-only.
	Auth access.Authorizer
	// Gate decides an agent's request after access allows it. A nil Gate denies
	// every agent request.
	Gate Gate
	// Check refuses an instantiated profile the node cannot run (for example a
	// transcript format it has no reader for) before anything is started. It
	// may be nil.
	Check func(core.Profile) error
	// Follow stops transcript followers; Surface reports a kill to its
	// requester. Both may be nil.
	Follow  Follower
	Surface Surface
	// Thresholds flag idle and unused agents; DefaultThresholds if zero.
	Thresholds registry.Thresholds
	// Now is the clock; time.Now if nil.
	Now func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// SpawnRequest asks for a temporary agent.
type SpawnRequest struct {
	// Recipe names the recipe to spawn from.
	Recipe string
	// Name names the agent. Empty generates one from the recipe.
	Name string
	// Channel names the channel its stream is bound to. Empty means the only
	// registered channel, and is an error when there are several.
	Channel   string
	Requester access.Subject
	// Authorized says the request was already authorized by whoever sent it, so it is
	// not decided again here. The master sets it when it commands a node: the master
	// has checked the access seam and the gate for the real requester, which it names
	// in Requester for the record. Nothing that takes a request from a caller sets it.
	Authorized bool
}

// KillRequest asks for a temporary agent to be torn down.
type KillRequest struct {
	Name      string
	Requester access.Subject
	// Authorized is as for SpawnRequest.
	Authorized bool
	// NoReport leaves the report of what was harvested to the caller, which has the
	// result: a node commanded by the master does not post it to its own operator.
	NoReport bool
}

// KillResult is what a kill produced.
type KillResult struct {
	Harvest harvest.Manifest
	// WorkdirKept is true when the workdir was not destroyed because the harvest
	// was incomplete or the removal failed. Nothing is lost: it is still there.
	WorkdirKept bool
}

// Status is one temporary agent as the operator sees it.
type Status struct {
	Name         string
	Recipe       string
	Channel      string
	State        core.State
	Flag         registry.Flag
	Created      time.Time
	LastActivity time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) lockFor(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	l, ok := m.locks[name]
	if !ok {
		l = &sync.Mutex{}
		m.locks[name] = l
	}
	return l
}

// authorize decides whether who may do action on res. The operator always may.
// An agent needs the access seam to allow it and then the approval gate to
// approve it; anything else, including a missing gate, is a refusal.
func (m *Manager) authorize(ctx context.Context, who access.Subject, action access.Action, res access.Resource, detail map[string]string) error {
	if who.Kind == access.KindOperator {
		return nil
	}
	if who.Kind != access.KindAgent || !access.Allowed(m.Auth, who, action, res) {
		return ErrNotFound
	}
	if m.Gate == nil {
		return fmt.Errorf("%w: no approval gate is configured", ErrDenied)
	}
	input, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDenied, err)
	}
	d := m.Gate.Decide(ctx, gate.Request{Agent: who.Name, Tool: "pao." + string(action), Input: input})
	if !d.Allow {
		return fmt.Errorf("%w: %s", ErrDenied, d.Reason)
	}
	return nil
}

// Spawn creates a temporary agent from a recipe and brings it up. It returns
// the agent's profile.
//
// If the agent does not become ready, it stays registered with its session left
// running for the operator to inspect, as for any agent (ADR-0007 point 2), and
// the error says so: killing it is the explicit way to remove it. Any other
// failure undoes everything the call did.
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (core.Profile, error) {
	set, err := m.Recipes()
	if err != nil {
		return core.Profile{}, fmt.Errorf("load recipes: %w", err)
	}
	rec, ok := set.Get(req.Recipe)
	if !ok {
		if !req.Authorized && req.Requester.Kind != access.KindOperator {
			// Which recipes exist is not for a caller to probe: to anyone but
			// the operator an unknown recipe is the same refusal as one they
			// may not use.
			return core.Profile{}, ErrNotFound
		}
		return core.Profile{}, fmt.Errorf("%w: %q", ErrNoRecipe, req.Recipe)
	}

	name := req.Name
	if name == "" {
		if name, err = generateName(rec.Name); err != nil {
			return core.Profile{}, err
		}
	}
	if !namePattern.MatchString(name) {
		return core.Profile{}, fmt.Errorf("%w: agent name %q must be lowercase letters, digits, '-' or '_'", ErrBadRequest, name)
	}
	if slices.Contains(core.ReservedNames, name) {
		return core.Profile{}, fmt.Errorf("%w: agent name %q is reserved", ErrBadRequest, name)
	}
	if !req.Authorized {
		if err := m.authorize(ctx, req.Requester, access.Spawn, access.Resource{Agent: name, Tags: rec.Profile.Tags},
			map[string]string{"recipe": rec.Name, "agent": name}); err != nil {
			return core.Profile{}, err
		}
	}

	channel, err := m.pickChannel(req.Channel)
	if err != nil {
		return core.Profile{}, err
	}

	lock := m.lockFor(name)
	lock.Lock()
	defer lock.Unlock()

	dir, err := m.Workdirs.Provision(ctx, name, rec.Source())
	if err != nil {
		return core.Profile{}, fmt.Errorf("provision workdir: %w", err)
	}
	undo := func() { _ = m.Workdirs.Remove(name) }

	prof, err := rec.Instantiate(name, dir, m.Locate)
	if err != nil {
		undo()
		return core.Profile{}, err
	}
	if m.Check != nil {
		if err := m.Check(prof); err != nil {
			undo()
			return core.Profile{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
	}
	if err := m.Registry.AddTemp(prof, registry.TempInfo{Created: m.now(), Channel: channel, Recipe: rec.Name}); err != nil {
		undo()
		return core.Profile{}, err
	}
	if err := m.Channels.Bind(name, channel); err != nil {
		_ = m.Registry.Remove(name)
		undo()
		return core.Profile{}, err
	}

	if _, err := m.Starter.Ensure(ctx, prof); err != nil {
		if _, notReady := control.AsReadinessError(err); notReady {
			return prof, fmt.Errorf("temporary agent %q is registered but not ready; its session is left running to inspect, and killing it removes it: %w", name, err)
		}
		// The launch failed outright: take back everything, session included.
		_ = m.Plane.Kill(ctx, prof)
		if m.Follow != nil {
			m.Follow.Stop(name)
		}
		m.Channels.Unbind(name)
		_ = m.Registry.Remove(name)
		undo()
		return core.Profile{}, err
	}
	return prof, nil
}

func (m *Manager) pickChannel(want string) (string, error) {
	if want != "" {
		if !m.Channels.HasChannel(want) {
			return "", fmt.Errorf("%w: no channel %q", ErrBadRequest, want)
		}
		return want, nil
	}
	all := m.Channels.Channels()
	switch len(all) {
	case 0:
		return "", fmt.Errorf("%w: no channel is registered to bind the agent to", ErrBadRequest)
	case 1:
		return all[0], nil
	default:
		return "", fmt.Errorf("%w: name a channel, one of %v", ErrBadRequest, all)
	}
}

func generateName(recipeName string) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return recipeName + "-" + hex.EncodeToString(b[:]), nil
}

// Kill tears a temporary agent down. The order is the point:
//
//  1. the agent's session is stopped, so nothing writes while its workdir is
//     copied and the control plane's own record is final;
//  2. its outputs are harvested, which is not optional;
//  3. only then is the workdir destroyed, and only if the harvest was complete;
//  4. the agent is unregistered and its requester told what was harvested.
//
// Whatever fails, nothing is lost: a failed harvest leaves the workdir and the
// registration in place for a retry, and an incomplete one keeps the workdir.
func (m *Manager) Kill(ctx context.Context, req KillRequest) (KillResult, error) {
	lock := m.lockFor(req.Name)
	lock.Lock()
	defer lock.Unlock()

	view, ok := m.Registry.TempOf(req.Name)
	if !ok {
		return KillResult{}, ErrNotFound
	}
	if !req.Authorized {
		if err := m.authorize(ctx, req.Requester, access.Kill, access.Resource{Agent: view.Profile.Name, Tags: view.Profile.Tags},
			map[string]string{"agent": view.Profile.Name}); err != nil {
			return KillResult{}, err
		}
	}

	if err := m.Plane.Kill(ctx, view.Profile); err != nil {
		return KillResult{}, fmt.Errorf("stop %q: %w", req.Name, err)
	}
	if err := m.Registry.SetState(req.Name, core.Stopped); err != nil {
		return KillResult{}, err
	}
	if m.Follow != nil {
		m.Follow.Stop(req.Name)
	}

	manifest, err := m.Harvests.Harvest(ctx, req.Name, view.Profile.Tags, view.Profile.Workdir, req.Requester)
	if err != nil {
		return KillResult{}, fmt.Errorf("harvest %q (its workdir and registration are kept, so the kill can be retried): %w", req.Name, err)
	}

	res := KillResult{Harvest: manifest, WorkdirKept: !manifest.Complete}
	if manifest.Complete {
		if err := m.Workdirs.Remove(req.Name); err != nil {
			slog.Warn("harvested, but the workdir could not be removed", "agent", req.Name, "error", err)
			res.WorkdirKept = true
		}
	}
	m.Channels.Unbind(req.Name)
	if err := m.Registry.Remove(req.Name); err != nil {
		return res, err
	}

	if m.Surface != nil && !req.NoReport {
		text := manifest.Summary()
		if res.WorkdirKept {
			text += "\nthe workdir was kept at " + m.Workdirs.Dir(req.Name)
		}
		m.Surface(ctx, req.Requester, text)
	}
	return res, nil
}

// List returns every temporary agent with its status flag, ordered by name.
func (m *Manager) List() []Status {
	th := m.Thresholds
	if th == (registry.Thresholds{}) {
		th = registry.DefaultThresholds
	}
	now := m.now()
	var out []Status
	for _, v := range m.Registry.Temps() {
		out = append(out, Status{
			Name: v.Profile.Name, Recipe: v.Info.Recipe, Channel: v.Info.Channel, State: v.State,
			Flag: v.Flag(now, th), Created: v.Info.Created, LastActivity: v.LastActivity,
		})
	}
	return out
}
