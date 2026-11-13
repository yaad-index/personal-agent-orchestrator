// Package tmux is the terminal driver (ADR-0002): it runs agents in tmux
// sessions, pastes input into them and reads their pane. It talks to a
// dedicated tmux server (its own socket), so pao never touches the operator's
// own tmux sessions.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is the tmux server socket name pao uses.
const DefaultSocket = "pao"

// SessionPrefix namespaces pao's sessions on the server.
const SessionPrefix = "pao-"

// ErrNoSession is returned when the named session does not exist.
var ErrNoSession = errors.New("no such session")

// Driver drives tmux. The zero value is not usable; use New.
type Driver struct {
	bin    string
	socket string
	// settle is how long to wait between a paste and the Enter that submits
	// it, so the terminal has taken the paste before the key arrives.
	settle time.Duration

	mu         sync.Mutex
	pasteLocks map[string]*sync.Mutex
}

// Option configures a Driver.
type Option func(*Driver)

// WithSocket sets the tmux server socket name.
func WithSocket(name string) Option { return func(d *Driver) { d.socket = name } }

// WithBinary sets the tmux executable.
func WithBinary(path string) Option { return func(d *Driver) { d.bin = path } }

// WithPasteSettle sets the pause between a paste and its submitting Enter.
func WithPasteSettle(dur time.Duration) Option { return func(d *Driver) { d.settle = dur } }

// New returns a Driver.
func New(opts ...Option) *Driver {
	d := &Driver{bin: "tmux", socket: DefaultSocket, settle: 200 * time.Millisecond, pasteLocks: make(map[string]*sync.Mutex)}
	for _, o := range opts {
		o(d)
	}
	return d
}

// SessionName is the tmux session name for an agent.
func SessionName(agent string) string { return SessionPrefix + agent }

// NewSession starts a detached session named for agent, running command in
// dir. It fails if the session already exists.
func (d *Driver) NewSession(ctx context.Context, agent, dir, command string) error {
	args := []string{"new-session", "-d", "-s", SessionName(agent)}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	args = append(args, command)
	_, err := d.run(ctx, nil, args...)
	return err
}

// HasSession reports whether the agent's session exists.
func (d *Driver) HasSession(ctx context.Context, agent string) (bool, error) {
	_, err := d.run(ctx, nil, "has-session", "-t", exact(agent))
	if err == nil {
		return true, nil
	}
	if isNoSession(err) {
		return false, nil
	}
	return false, err
}

// KillSession ends the agent's session.
func (d *Driver) KillSession(ctx context.Context, agent string) error {
	_, err := d.run(ctx, nil, "kill-session", "-t", exact(agent))
	return err
}

// Capture returns the visible text of the agent's pane.
func (d *Driver) Capture(ctx context.Context, agent string) (string, error) {
	out, err := d.run(ctx, nil, "capture-pane", "-p", "-t", pane(agent))
	return string(out), err
}

// Paste pastes text into the agent's terminal as one bracketed paste and then
// submits it with Enter. The text goes through a tmux buffer, never through
// shell or key parsing, so it arrives verbatim. Pastes to one agent are
// serialized: the buffer is per agent and a paste plus its Enter is one unit,
// so two inputs arriving together reach the terminal one after the other
// instead of interleaving.
func (d *Driver) Paste(ctx context.Context, agent, text string) error {
	lock := d.pasteLock(agent)
	lock.Lock()
	defer lock.Unlock()

	buf := SessionName(agent)
	if _, err := d.run(ctx, strings.NewReader(text), "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	if _, err := d.run(ctx, nil, "paste-buffer", "-p", "-d", "-b", buf, "-t", pane(agent)); err != nil {
		return err
	}
	select {
	case <-time.After(d.settle):
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := d.run(ctx, nil, "send-keys", "-t", pane(agent), "Enter")
	return err
}

func (d *Driver) pasteLock(agent string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.pasteLocks[agent]
	if !ok {
		l = &sync.Mutex{}
		d.pasteLocks[agent] = l
	}
	return l
}

// exact makes tmux match the session name exactly rather than by prefix.
func exact(agent string) string { return "=" + SessionName(agent) }

// pane targets the active pane of the agent's session, matching the session
// name exactly (a pane target needs the trailing colon).
func pane(agent string) string { return exact(agent) + ":" }

type runError struct {
	args   []string
	stderr string
	err    error
}

func (e *runError) Error() string {
	return fmt.Sprintf("tmux %s: %v: %s", e.args[0], e.err, strings.TrimSpace(e.stderr))
}

func (e *runError) Unwrap() error { return e.err }

func isNoSession(err error) bool {
	var re *runError
	if !errors.As(err, &re) {
		return false
	}
	s := re.stderr
	return strings.Contains(s, "can't find session") ||
		strings.Contains(s, "no server running") ||
		strings.Contains(s, "error connecting to")
}

func (d *Driver) run(ctx context.Context, stdin *strings.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin, append([]string{"-L", d.socket}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &runError{args: args, stderr: stderr.String(), err: err}
	}
	return stdout.Bytes(), nil
}
