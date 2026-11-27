package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	"github.com/yaad-index/personal-agent-orchestrator/internal/shell"
)

// tokenPattern is what agenttoken mints: 256 bits in hex.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// agentTokenFiles hands each agent's token to its session through a file in
// pao's state that only the node user can read. A value in a session's
// environment given on the multiplexer's command line is readable by any local
// user, so the token never goes there: the session's command reads the file
// into the agent's environment before it starts the agent.
type agentTokenFiles struct{ dir string }

func (f agentTokenFiles) path(agent string) string { return filepath.Join(f.dir, agent) }

// prepare makes the directory, readable by the node user alone. What is in it
// is kept: an agent whose session outlived pao still holds its token
// (ADR-0051 point 3).
func (f agentTokenFiles) prepare() error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(f.dir, 0o700)
}

// read is agent's token as it was handed to its session.
func (f agentTokenFiles) read(agent string) (string, error) {
	b, err := os.ReadFile(f.path(agent))
	if err != nil {
		return "", err
	}
	tok := string(b)
	if !tokenPattern.MatchString(tok) {
		return "", fmt.Errorf("agent %q: its token file does not hold a token", agent)
	}
	return tok, nil
}

// prune removes every token file except those of the agents keep names.
func (f agentTokenFiles) prune(keep map[string]bool) error {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(f.dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// write puts agent's token in its file, readable by the node user alone,
// replacing any earlier one whole.
func (f agentTokenFiles) write(agent, token string) error {
	tmp, err := os.CreateTemp(f.dir, "."+agent+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(token); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(agent))
}

// remove forgets agent's token. A token that is not there is not an error.
func (f agentTokenFiles) remove(agent string) error {
	if err := os.Remove(f.path(agent)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// load is command preceded by reading agent's token into the environment the
// agent starts with. If the token can't be read the agent is not started, so
// the failure shows rather than an agent that pao refuses.
func (f agentTokenFiles) load(agent, command string) string {
	return fmt.Sprintf("%s=\"$(cat -- %s)\" && export %s && %s",
		backend.EnvAgentToken, shell.Quote(f.path(agent)), backend.EnvAgentToken, command)
}
