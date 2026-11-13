// Package workdir provisions the isolated working directory of a temporary
// agent (ADR-0006 point 2). Each agent gets a directory of its own under one
// root, made from a source: a git clone or a set of pushed files. The
// directory is created fresh, never reused, and is removed only by an explicit
// call after the agent's outputs are harvested. Isolation here is separate
// working state, not a sandbox: the agent still runs as the node's user.
package workdir

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Limits on pushed files. They bound what one spawn request can write; a
// recipe that needs more clones a repository instead.
const (
	MaxFiles = 1000
	MaxBytes = 16 << 20
)

// DefaultCloneTimeout bounds a git clone.
const DefaultCloneTimeout = 5 * time.Minute

var (
	// ErrExists is returned when the agent's directory already exists.
	ErrExists = errors.New("workdir already exists")
	// ErrBadSource is returned for a source that is malformed or unsafe.
	ErrBadSource = errors.New("bad workdir source")
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Git is a git clone source.
type Git struct {
	// URL is the repository to clone.
	URL string
	// Ref is a branch or tag to check out. Empty means the default branch.
	Ref string
}

// Source says what a workdir is made from. Exactly one of Git and Files is set.
type Source struct {
	Git *Git
	// Files are pushed files: slash-separated relative path to content.
	Files map[string][]byte
}

// Provisioner makes and removes workdirs under Root.
type Provisioner struct {
	// Root holds one directory per agent. It is created if missing.
	Root string
	// AllowLocal permits cloning from the local file system (file:// URLs and
	// plain paths). Off by default: a recipe pulled from elsewhere must not be
	// able to copy a repository off this machine into an agent's reach.
	AllowLocal bool
	// CloneTimeout bounds a clone; DefaultCloneTimeout if zero.
	CloneTimeout time.Duration
}

// Dir returns the directory the named agent's workdir lives in. It does not
// check that the name is valid; Provision and Remove do.
func (p *Provisioner) Dir(name string) string { return filepath.Join(p.Root, name) }

// Provision creates the named agent's workdir from src and returns its path.
// The directory must not already exist, so an agent never inherits another's
// files. On failure nothing is left behind.
func (p *Provisioner) Provision(ctx context.Context, name string, src Source) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%w: agent name %q", ErrBadSource, name)
	}
	if err := checkSource(src); err != nil {
		return "", err
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return "", err
	}
	dir := p.Dir(name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrExists, name)
		}
		return "", err
	}

	var err error
	if src.Git != nil {
		err = p.clone(ctx, dir, *src.Git)
	} else {
		err = writeFiles(dir, src.Files)
	}
	if err != nil {
		// The directory was created above by this call and is ours to remove.
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// Remove deletes the named agent's workdir. Removing one that does not exist
// is not an error. Callers harvest first (ADR-0006 point 4).
func (p *Provisioner) Remove(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: agent name %q", ErrBadSource, name)
	}
	return os.RemoveAll(p.Dir(name))
}

func checkSource(src Source) error {
	switch {
	case src.Git != nil && src.Files != nil:
		return fmt.Errorf("%w: give a git source or files, not both", ErrBadSource)
	case src.Git == nil && src.Files == nil:
		return fmt.Errorf("%w: no source", ErrBadSource)
	}
	if src.Git != nil {
		if src.Git.URL == "" {
			return fmt.Errorf("%w: git url is empty", ErrBadSource)
		}
		// A leading dash would be read by git as an option, not a location.
		if strings.HasPrefix(src.Git.URL, "-") || strings.HasPrefix(src.Git.Ref, "-") {
			return fmt.Errorf("%w: git url and ref must not start with '-'", ErrBadSource)
		}
	}
	return nil
}

func (p *Provisioner) clone(ctx context.Context, dir string, g Git) error {
	if !p.AllowLocal && isLocal(g.URL) {
		return fmt.Errorf("%w: local git sources are not allowed", ErrBadSource)
	}
	timeout := p.CloneTimeout
	if timeout == 0 {
		timeout = DefaultCloneTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"clone", "--depth", "1", "--no-tags"}
	if g.Ref != "" {
		args = append(args, "--branch", g.Ref)
	}
	args = append(args, "--", g.URL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)

	protocols := "https:ssh:git"
	if p.AllowLocal {
		protocols += ":file"
	}
	// No prompt for credentials (the clone would hang), and only the
	// transports named: git's external-command transport is never reachable.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL="+protocols)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		shown := redact(g.URL)
		if ctx.Err() != nil {
			return fmt.Errorf("git clone %s: %w", shown, ctx.Err())
		}
		// git echoes the location it was given; keep a password out of the error.
		msg := strings.ReplaceAll(strings.TrimSpace(out.String()), g.URL, shown)
		return fmt.Errorf("git clone %s: %w: %s", shown, err, msg)
	}
	return nil
}

// redact drops any password from the userinfo of a URL, for use in errors.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}

// isLocal reports whether a git location is on this machine: a file:// URL or
// a path rather than a scheme or scp-style host.
func isLocal(url string) bool {
	if strings.HasPrefix(url, "file:") {
		return true
	}
	if strings.Contains(url, "://") {
		return false
	}
	// scp-style user@host:path has a colon before any slash.
	if i := strings.Index(url, ":"); i >= 0 && !strings.Contains(url[:i], "/") {
		return false
	}
	return true
}

func writeFiles(dir string, files map[string][]byte) error {
	if len(files) > MaxFiles {
		return fmt.Errorf("%w: %d files, at most %d", ErrBadSource, len(files), MaxFiles)
	}
	total := 0
	for name, content := range files {
		if err := checkPath(name); err != nil {
			return err
		}
		total += len(content)
		if total > MaxBytes {
			return fmt.Errorf("%w: files exceed %d bytes", ErrBadSource, MaxBytes)
		}
	}
	for name, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		// O_EXCL: never write through something already there, which a
		// duplicate or case-folded path would otherwise do.
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		_, werr := f.Write(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("write %s: %w", name, werr)
		}
	}
	return nil
}

// checkPath accepts only a clean, relative, slash-separated path that stays
// inside the workdir.
func checkPath(name string) error {
	if name == "" || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: file path %q", ErrBadSource, name)
	}
	if path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || name == ".." {
		return fmt.Errorf("%w: file path %q must be clean and relative", ErrBadSource, name)
	}
	return nil
}
