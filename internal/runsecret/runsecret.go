// Package runsecret materializes a task's secret for the one run that needs it
// (ADR-0036 point 3): its value goes into a file only the node user can read,
// in a directory of the agent's own outside its working directory, and the
// agent is given the file's path in its environment, never the value. The
// directory is removed when the agent's session ends, by a kill, a stop or a
// crash, and every leftover is swept when the node starts.
package runsecret

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// EnvPrefix begins the variable that names a secret's file.
const EnvPrefix = "PAO_SECRET_"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// EnvName is the variable a secret's file path is given in: PAO_SECRET_ and the
// name in upper case, with '-' as '_'.
func EnvName(secret string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(secret, "-", "_"))
}

// Store keeps the run secrets under Dir, one directory per agent.
type Store struct {
	Dir string
	// Resolve returns a secret's value from where the orchestrator config says
	// it is held. Its error never carries the value.
	Resolve func(name string) ([]byte, error)
}

func (s *Store) agentDir(agent string) (string, error) {
	if !namePattern.MatchString(agent) {
		return "", fmt.Errorf("agent name %q", agent)
	}
	return filepath.Join(s.Dir, agent), nil
}

// Materialize writes the secret's value for agent and returns the variable and
// the path to give it. The agent's directory must not exist yet: a run's
// secrets are made once, before it starts, and never added to another's.
func (s *Store) Materialize(agent, secret string) (string, error) {
	if !namePattern.MatchString(secret) {
		return "", fmt.Errorf("secret name %q", secret)
	}
	dir, err := s.agentDir(agent)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("run secrets for %s: %w", agent, err)
	}
	value, err := s.Resolve(secret)
	if err == nil && len(value) == 0 {
		err = fmt.Errorf("secret %q is empty", secret)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	path := filepath.Join(dir, secret)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	_, werr := f.Write(value)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// Env returns the variables naming agent's secret files, one per secret.
func (s *Store) Env(agent string) []string {
	dir, err := s.agentDir(agent)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && namePattern.MatchString(e.Name()) {
			out = append(out, EnvName(e.Name())+"="+filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// Wipe removes agent's secrets. Having none is not an error.
func (s *Store) Wipe(agent string) error {
	dir, err := s.agentDir(agent)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Sweep removes every agent's secrets: at start no run is under way, so any
// left is from a run that ended without its wipe.
func (s *Store) Sweep() error {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(s.Dir, e.Name())))
	}
	return errors.Join(errs...)
}
