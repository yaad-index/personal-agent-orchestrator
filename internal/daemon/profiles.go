package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"go.yaml.in/yaml/v3"
)

// profileFiles reads and writes the node's agent profiles for the management
// UI (ADR-0047 point 2), and applies what it writes: a new profile becomes an
// agent at once, a stopped agent takes its new profile at once, and a running
// agent keeps the profile it started with until it stops (ADR-0051).
//
// A profile is addressed by its file's name. Each file holds one profile, as
// the node loads them at start; a file is written whole, so its own comments
// and layout are kept exactly as the operator wrote them.
type profileFiles struct {
	dir  string
	orch config.Orchestrator
	reg  *registry.Registry
	// check is what else a profile must pass before the node runs it, such as
	// a transcript format it can read.
	check func(core.Profile) error
	// locate is where a backend keeps an agent's transcripts, for a draft.
	locate func(format, workdir string) (string, bool)

	mu sync.Mutex
	// pending are profiles saved while their agent ran, applied when it stops.
	pending map[string]core.Profile
}

var _ local.ProfilesAPI = (*profileFiles)(nil)

func newProfileFiles(dir string, orch config.Orchestrator, reg *registry.Registry, check func(core.Profile) error) *profileFiles {
	return &profileFiles{dir: dir, orch: orch, reg: reg, check: check, locate: locate, pending: map[string]core.Profile{}}
}

// fileName is a profile file's name: a plain name ending in .yaml, never a path.
func fileName(file string) error {
	if !strings.HasSuffix(file, ".yaml") || file != filepath.Base(file) || strings.HasPrefix(file, ".") || strings.ContainsAny(file, `/\`) {
		return fmt.Errorf("%w: %q", local.ErrNotFound, file)
	}
	return nil
}

func (f *profileFiles) parse(b []byte) (core.Profile, error) {
	p, err := config.ParseProfile(b, f.orch)
	if err == nil && f.check != nil {
		err = f.check(p)
	}
	if err != nil {
		return core.Profile{}, fmt.Errorf("%w: %v", local.ErrInvalid, err)
	}
	return p, nil
}

// view is a profile file as the UI lists it, read with f.mu held.
func (f *profileFiles) view(file string, b []byte) local.ProfileJSON {
	v := local.ProfileJSON{File: file, Tags: []string{}}
	p, err := f.parse(b)
	if err != nil {
		v.Status, v.Error = local.ProfileInvalid, strings.TrimPrefix(err.Error(), local.ErrInvalid.Error()+": ")
		return v
	}
	v.Name, v.Backend = p.Name, p.Backend
	if p.Tags != nil {
		v.Tags = slices.Clone(p.Tags)
	}
	running, state, ok := f.reg.Lookup(p.Name)
	v.Running = ok && state == core.Running
	switch pending, isPending := f.pending[p.Name]; {
	case ok && reflect.DeepEqual(running, p):
		v.Status = local.ProfileLoaded
	case isPending && reflect.DeepEqual(pending, p):
		v.Status = local.ProfileRestart
	default:
		v.Status = local.ProfileNotApplied
	}
	return v
}

func (f *profileFiles) List() ([]local.ProfileJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(f.dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := []local.ProfileJSON{}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, f.view(filepath.Base(path), b))
	}
	return out, nil
}

func (f *profileFiles) Get(file string) (local.ProfileFileJSON, error) {
	if err := fileName(file); err != nil {
		return local.ProfileFileJSON{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(f.dir, file))
	if errors.Is(err, os.ErrNotExist) {
		return local.ProfileFileJSON{}, fmt.Errorf("%w: %q", local.ErrNotFound, file)
	}
	if err != nil {
		return local.ProfileFileJSON{}, err
	}
	return local.ProfileFileJSON{ProfileJSON: f.view(file, b), Text: string(b)}, nil
}

func (f *profileFiles) Create(text string) (local.ProfileJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.parse([]byte(text))
	if err != nil {
		return local.ProfileJSON{}, err
	}
	file := p.Name + ".yaml"
	if _, err := os.Stat(filepath.Join(f.dir, file)); err == nil {
		return local.ProfileJSON{}, fmt.Errorf("%w: a profile file %s", local.ErrExists, file)
	}
	if err := f.nameFree(p.Name, ""); err != nil {
		return local.ProfileJSON{}, err
	}
	if err := writeFile(filepath.Join(f.dir, file), []byte(text), 0o644); err != nil {
		return local.ProfileJSON{}, err
	}
	if err := f.apply(p); err != nil {
		return local.ProfileJSON{}, err
	}
	return f.view(file, []byte(text)), nil
}

func (f *profileFiles) Save(file, text string) (local.ProfileJSON, error) {
	if err := fileName(file); err != nil {
		return local.ProfileJSON{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := filepath.Join(f.dir, file)
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return local.ProfileJSON{}, fmt.Errorf("%w: %q", local.ErrNotFound, file)
	}
	if err != nil {
		return local.ProfileJSON{}, err
	}
	p, err := f.parse([]byte(text))
	if err != nil {
		return local.ProfileJSON{}, err
	}
	// A profile keeps its agent's name: a new name is a new agent, made as a
	// new profile, and the old agent is left as it is.
	if before, err := f.parse(old); err == nil && before.Name != p.Name {
		return local.ProfileJSON{}, fmt.Errorf("%w: this file defines agent %q; a new name is a new profile", local.ErrInvalid, before.Name)
	} else if err != nil {
		if err := f.nameFree(p.Name, file); err != nil {
			return local.ProfileJSON{}, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return local.ProfileJSON{}, err
	}
	if err := writeFile(path, []byte(text), info.Mode().Perm()); err != nil {
		return local.ProfileJSON{}, err
	}
	if err := f.apply(p); err != nil {
		return local.ProfileJSON{}, err
	}
	return f.view(file, []byte(text)), nil
}

// nameFree refuses a name another profile file, or an agent the node runs,
// already has; except names the file being saved.
func (f *profileFiles) nameFree(name, except string) error {
	files, err := filepath.Glob(filepath.Join(f.dir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, path := range files {
		if filepath.Base(path) == except {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if p, err := f.parse(b); err == nil && p.Name == name {
			return fmt.Errorf("%w: agent %q is defined in %s", local.ErrExists, name, filepath.Base(path))
		}
	}
	if _, _, ok := f.reg.Lookup(name); ok {
		if _, temp := f.reg.TempOf(name); temp {
			return fmt.Errorf("%w: a temporary agent is named %q", local.ErrExists, name)
		}
	}
	return nil
}

// apply puts p into the node: a new agent is added stopped, a stopped one
// takes p at once, and a running one keeps its profile until it stops, when
// takePending hands p over.
func (f *profileFiles) apply(p core.Profile) error {
	_, state, ok := f.reg.Lookup(p.Name)
	switch {
	case !ok:
		delete(f.pending, p.Name)
		return f.reg.Add(p)
	case state == core.Running:
		f.pending[p.Name] = p
		return nil
	default:
		delete(f.pending, p.Name)
		return f.reg.Replace(p)
	}
}

// takePending is a profile saved while the agent ran, now that it has
// stopped, and forgets it.
func (f *profileFiles) takePending(name string) (core.Profile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pending[name]
	delete(f.pending, name)
	return p, ok
}

// writeFile replaces path with b whole: a reader sees the old file or the new
// one, never part of either.
func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var (
	draftName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	draftModel = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,63}$`)
)

// Draft is a new Claude Code profile's starting text: commented, with the
// transcript where Claude Code writes it for that working directory. It is
// checked as a saved profile would be, so it is one the node would load.
func (f *profileFiles) Draft(d local.ProfileDraftJSON) (string, error) {
	switch {
	case !draftName.MatchString(d.Name):
		return "", fmt.Errorf("%w: name %q must be lowercase letters, digits, '-' or '_'", local.ErrInvalid, d.Name)
	case !filepath.IsAbs(d.Workdir) || filepath.Clean(d.Workdir) != d.Workdir:
		return "", fmt.Errorf("%w: workdir must be an absolute path", local.ErrInvalid)
	case d.Model != "" && !draftModel.MatchString(d.Model):
		return "", fmt.Errorf("%w: model %q is not a model name", local.ErrInvalid, d.Model)
	}
	for _, t := range d.Tags {
		if !draftName.MatchString(t) {
			return "", fmt.Errorf("%w: tag %q must be lowercase letters, digits, '-' or '_'", local.ErrInvalid, t)
		}
	}
	transcript, ok := f.locate(claudecode.Format, d.Workdir)
	if !ok {
		return "", fmt.Errorf("%w: the node cannot tell where Claude Code keeps transcripts", local.ErrInvalid)
	}
	command := "claude"
	if d.Model != "" {
		command += " --model " + d.Model
	}
	q := func(s string) string {
		b, _ := yaml.Marshal(s)
		return strings.TrimSpace(string(b))
	}
	text := "# " + d.Name + ": an agent run with Claude Code.\n" +
		"name: " + d.Name + "\n" +
		"tags: [" + strings.Join(d.Tags, ", ") + "]\n" +
		"backend: claude-code\n" +
		"workdir: " + q(d.Workdir) + "\n" +
		"# The agent loads only its working directory, pao's MCP configuration and\n" +
		"# pao's gate hook: nothing from the node user's own Claude setup (ADR-0025).\n" +
		"inherit: []\n" +
		"startup:\n" +
		"  command: " + q(command) + "\n" +
		"  ready_pattern: " + q(claudeReadyPattern) + "\n" +
		"  ready_timeout: 60s\n" +
		"transcript:\n" +
		"  format: " + claudecode.Format + "\n" +
		"  path: " + q(transcript) + "\n"
	if _, err := f.parse([]byte(text)); err != nil {
		return "", err
	}
	return text, nil
}

// claudeReadyPattern is what Claude Code's pane shows once it takes input.
const claudeReadyPattern = `for shortcuts|bypass permissions|Try "`

// configuredIn reports whether an agent is one the operator's profiles define,
// those added from the management UI after start included, and never a
// temporary agent.
func configuredIn(reg *registry.Registry) func(string) bool {
	return func(name string) bool {
		_, _, ok := reg.Lookup(name)
		_, temp := reg.TempOf(name)
		return ok && !temp
	}
}
