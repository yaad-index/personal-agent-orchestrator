package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	"github.com/yaad-index/personal-agent-orchestrator/internal/shell"
)

// agentTokenFiles hands each agent's token to its session through a file in
// pao's state that only the node user can read. A value in a session's
// environment given on the multiplexer's command line is readable by any local
// user, so the token never goes there: the session's command reads the file
// into the agent's environment before it starts the agent.
type agentTokenFiles struct{ dir string }

func (f agentTokenFiles) path(agent string) string { return filepath.Join(f.dir, agent) }

// reset empties the directory, made readable by the node user alone.
func (f agentTokenFiles) reset() error {
	if err := os.RemoveAll(f.dir); err != nil {
		return err
	}
	return os.MkdirAll(f.dir, 0o700)
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
