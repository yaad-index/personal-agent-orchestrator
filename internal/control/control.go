// Package control is the node's control plane (ADR-0007): it owns agent
// lifecycle through the terminal driver. This file covers bringing an agent up,
// delivering input to it, and taking it down; context watch and liveness
// arrive with the milestones that read the backend's own signals.
package control

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Terminal is the terminal driver the control plane runs agents through. The
// tmux driver implements it (ADR-0002).
type Terminal interface {
	NewSession(ctx context.Context, agent, dir, command string) error
	HasSession(ctx context.Context, agent string) (bool, error)
	KillSession(ctx context.Context, agent string) error
	Capture(ctx context.Context, agent string) (string, error)
	Paste(ctx context.Context, agent, text string) error
}

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
}

// Option configures a Plane.
type Option func(*Plane)

// WithPollInterval sets how often startup checks the pane for readiness.
func WithPollInterval(d time.Duration) Option { return func(p *Plane) { p.poll = d } }

// New returns a Plane.
func New(term Terminal, opts ...Option) *Plane {
	p := &Plane{term: term, poll: 500 * time.Millisecond}
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
		if err := p.term.NewSession(ctx, prof.Name, prof.Workdir, prof.Startup.Command); err != nil {
			return err
		}
	}

	deadline := time.Now().Add(prof.Startup.ReadyTimeout)
	for {
		pane, err := p.term.Capture(ctx, prof.Name)
		if err != nil {
			return err
		}
		if ready.MatchString(pane) {
			return nil
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
	if !exists {
		return nil
	}
	return p.term.KillSession(ctx, prof.Name)
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
