// Package control is the node's control plane (ADR-0007): it owns agent
// lifecycle through the terminal driver. This file covers bringing an agent up,
// delivering input to it, and taking it down; context watch and liveness
// arrive with the milestones that read the backend's own signals.
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Terminal is the terminal driver the control plane runs agents through. The
// tmux driver implements it (ADR-0002).
type Terminal interface {
	NewSession(ctx context.Context, agent, dir, command string, env []string) error
	HasSession(ctx context.Context, agent string) (bool, error)
	KillSession(ctx context.Context, agent string) error
	Capture(ctx context.Context, agent string) (string, error)
	Paste(ctx context.Context, agent, text string) error
	// SendKeys sends terminal keys, such as "Down" or "Enter", to the agent's
	// pane.
	SendKeys(ctx context.Context, agent string, keys ...string) error
}

// Prompt is a question an agent's backend may stop at while it starts, and the
// keys that answer it.
type Prompt struct {
	About   string
	Pattern *regexp.Regexp
	Keys    []string
	// Tries is how many times the keys may be sent while the question stays
	// shown; zero means once. A backend may draw a question before it takes
	// keys, so a key can be lost; a question whose pattern names the state its
	// keys apply in can safely be answered again.
	Tries int
}

// DefaultSettle is how long a question must have been shown before it is
// answered, and before it is answered again.
const DefaultSettle = time.Second

// ReadinessError reports that an agent did not reach readiness in time. Its
// Error text never carries the pane: the raw pane is for the operator alone
// (ADR-0007 point 2), reached only through Pane, so it cannot leak into a log
// line, a routed message or a reply by way of the error string.
type ReadinessError struct {
	Agent   string
	Timeout time.Duration
	pane    string
}

func (e *ReadinessError) Error() string {
	return fmt.Sprintf("agent %q not ready after %s", e.Agent, e.Timeout)
}

// Pane returns the raw terminal pane captured when readiness timed out.
func (e *ReadinessError) Pane() string { return e.pane }

// Plane implements core.Launcher and core.Deliverer over a Terminal.
type Plane struct {
	term Terminal
	poll time.Duration
	env  func(agent string) []string
	// command builds the shell command that starts an agent and the
	// environment entries it adds; nil runs the profile's own command.
	command func(core.Profile) (string, []string, error)
	// onStop is called when an agent's session is known to be over.
	onStop func(agent string)
	// prompts are the start-up questions to answer for an agent; nil answers none.
	prompts func(core.Profile) []Prompt
	// settle is how long a question must be shown before it is answered.
	settle time.Duration
	// clock tells the time start-up questions are timed by.
	clock func() time.Time
}

// WithSettle sets how long a start-up question must have been shown before it
// is answered, and before an answer that did not take is sent again.
func WithSettle(d time.Duration) Option { return func(p *Plane) { p.settle = d } }

// WithPrompts sets the start-up questions the plane answers for an agent while
// waiting for it to be ready. Each is answered at most once per start: one that
// comes back is left, and the start times out as any other that is not ready.
func WithPrompts(f func(core.Profile) []Prompt) Option { return func(p *Plane) { p.prompts = f } }

// Option configures a Plane.
type Option func(*Plane)

// WithPollInterval sets how often startup checks the pane for readiness.
func WithPollInterval(d time.Duration) Option { return func(p *Plane) { p.poll = d } }

// WithEnv sets the environment (KEY=value entries) every agent is started with;
// f is called with the agent's name. It is how an agent learns who it is and
// where its node's gate is.
func WithEnv(f func(agent string) []string) Option { return func(p *Plane) { p.env = f } }

// WithCommand sets the function that builds, from an agent's profile, the shell
// command it is started with and the environment entries (KEY=value) it gets
// besides those of WithEnv. It is how a backend adapter adds what it owns
// (ADR-0025 point 2). An error starts nothing.
func WithCommand(f func(core.Profile) (string, []string, error)) Option {
	return func(p *Plane) { p.command = f }
}

// WithOnStop sets a function called with an agent's name once its session is
// ended by Kill, whether Kill ended it or found it already gone. It is how what
// is tied to a live session (an agent's credential) ends with it.
func WithOnStop(f func(agent string)) Option { return func(p *Plane) { p.onStop = f } }

// New returns a Plane.
func New(term Terminal, opts ...Option) *Plane {
	p := &Plane{term: term, poll: 500 * time.Millisecond, settle: DefaultSettle, clock: time.Now}
	for _, o := range opts {
		o(p)
	}
	return p
}

var (
	_ core.Launcher  = (*Plane)(nil)
	_ core.Deliverer = (*Plane)(nil)
)

// Launch runs the profile's startup procedure and returns once the pane
// matches its ready pattern. If the agent's session already exists (an
// earlier launch that timed out) it is not started a second time; readiness is
// checked again. A session that does not become ready is left running for the
// operator to inspect, not killed: removing an agent is always explicit
// (ADR-0007 point 5).
func (p *Plane) Launch(ctx context.Context, prof core.Profile) error {
	ready, err := regexp.Compile(prof.Startup.ReadyPattern)
	if err != nil {
		return fmt.Errorf("startup.ready_pattern: %w", err)
	}

	exists, err := p.term.HasSession(ctx, prof.Name)
	if err != nil {
		return err
	}
	if !exists {
		command := prof.Startup.Command
		var extra []string
		if p.command != nil {
			if command, extra, err = p.command(prof); err != nil {
				return fmt.Errorf("agent %q: %w", prof.Name, err)
			}
		}
		var env []string
		if p.env != nil {
			env = p.env(prof.Name)
		}
		env = append(env, extra...)
		if err := p.term.NewSession(ctx, prof.Name, prof.Workdir, command, env); err != nil {
			return err
		}
	}

	var prompts []Prompt
	if p.prompts != nil {
		prompts = p.prompts(prof)
	}
	// For each question: how often it was answered, since when it has been
	// shown without a break, and when it was last answered.
	answered := make([]int, len(prompts))
	shown := make([]time.Time, len(prompts))
	last := make([]time.Time, len(prompts))
	deadline := time.Now().Add(prof.Startup.ReadyTimeout)
	for {
		pane, err := p.term.Capture(ctx, prof.Name)
		if err != nil {
			return err
		}
		if ready.MatchString(pane) {
			return nil
		}
		now := p.clock()
		for i, q := range prompts {
			if !q.Pattern.MatchString(pane) {
				shown[i] = time.Time{}
				continue
			}
			if shown[i].IsZero() {
				shown[i] = now
			}
			tries := max(q.Tries, 1)
			// Answered only once it has been shown a while, so its keys reach a
			// question that takes them; again only a while after an answer that
			// left it shown.
			if answered[i] >= tries || now.Sub(shown[i]) < p.settle || (answered[i] > 0 && now.Sub(last[i]) < p.settle) {
				continue
			}
			answered[i]++
			last[i] = now
			slog.Info("answering a start-up question", "agent", prof.Name, "question", q.About, "try", answered[i])
			if err := p.term.SendKeys(ctx, prof.Name, q.Keys...); err != nil {
				return fmt.Errorf("agent %q: answering %q: %w", prof.Name, q.About, err)
			}
		}
		if !time.Now().Before(deadline) {
			return &ReadinessError{Agent: prof.Name, Timeout: prof.Startup.ReadyTimeout, pane: pane}
		}
		select {
		case <-time.After(p.poll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Deliver pastes the message into the agent's terminal as `[sender]: text`.
func (p *Plane) Deliver(ctx context.Context, prof core.Profile, m core.Message) error {
	return p.term.Paste(ctx, prof.Name, m.Line())
}

// Kill ends the agent's session. It is an explicit act, never automatic
// (ADR-0007 point 5). Killing an agent that has no session is not an error.
func (p *Plane) Kill(ctx context.Context, prof core.Profile) error {
	exists, err := p.term.HasSession(ctx, prof.Name)
	if err != nil {
		return err
	}
	if exists {
		if err := p.term.KillSession(ctx, prof.Name); err != nil {
			return err
		}
	}
	if p.onStop != nil {
		p.onStop(prof.Name)
	}
	return nil
}

// Alive reports whether the agent has a session. An error counts as not alive:
// it is asked to decide whether something may be trusted, and a question that
// cannot be answered is not a yes.
func (p *Plane) Alive(ctx context.Context, agent string) bool {
	ok, err := p.term.HasSession(ctx, agent)
	return err == nil && ok
}

// Restart kills the agent's session and launches it again.
func (p *Plane) Restart(ctx context.Context, prof core.Profile) error {
	if err := p.Kill(ctx, prof); err != nil {
		return err
	}
	return p.Launch(ctx, prof)
}

// AsReadinessError extracts a *ReadinessError from err, for a caller that
// wants to show the operator the pane.
func AsReadinessError(err error) (*ReadinessError, bool) {
	var re *ReadinessError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}
