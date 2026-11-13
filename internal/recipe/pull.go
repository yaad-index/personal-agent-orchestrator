package recipe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

// ErrSetExists is returned when a set of that name is already installed and
// replacing it was not asked for.
var ErrSetExists = errors.New("recipe set already installed")

// PullOptions say where a set comes from and where it goes.
type PullOptions struct {
	// Dir is the recipes directory the set is installed under.
	Dir string
	// URL and Ref name the git repository and the branch or tag.
	URL, Ref string
	// Set names the set's directory. Empty derives it from the URL.
	Set string
	// Replace overwrites an installed set of the same name.
	Replace bool
	// AllowLocal permits a repository on this machine.
	AllowLocal bool
}

var nonName = regexp.MustCompile(`[^a-z0-9_-]+`)

// DefaultSetName derives a set name from a repository location: its last path
// element without .git, lowercased, with anything else turned into '-'.
func DefaultSetName(url string) string {
	base := strings.TrimRight(url, "/")
	if i := strings.LastIndexAny(base, "/:"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".git")
	base = strings.Trim(nonName.ReplaceAllString(strings.ToLower(base), "-"), "-_")
	return base
}

// Pull clones a git repository into a set directory under opts.Dir and returns
// the names of the recipes it holds. The clone is made aside and checked first:
// every recipe in it must load, and none may reuse the name of a recipe in
// another installed set. Only then is it moved into place, so a repository that
// is broken, empty or ambiguous installs nothing, and a failed replace leaves
// the old set as it was.
func Pull(ctx context.Context, opts PullOptions) ([]string, error) {
	set := opts.Set
	if set == "" {
		set = DefaultSetName(opts.URL)
	}
	if !namePattern.MatchString(set) {
		return nil, fmt.Errorf("recipe set name %q must be lowercase letters, digits, '-' or '_' (give one with a name option)", set)
	}
	dest := filepath.Join(opts.Dir, set)
	if _, err := os.Lstat(dest); err == nil && !opts.Replace {
		return nil, fmt.Errorf("%w: %s", ErrSetExists, set)
	}

	// The clone is made under a dot directory, which loading skips, so a
	// crash leaves nothing that looks like an installed set.
	prov := &workdir.Provisioner{Root: filepath.Join(opts.Dir, ".pull"), AllowLocal: opts.AllowLocal}
	if err := prov.Remove(set); err != nil {
		return nil, err
	}
	asideDir, err := prov.Provision(ctx, set, workdir.Source{Git: &workdir.Git{URL: opts.URL, Ref: opts.Ref}})
	if err != nil {
		return nil, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = prov.Remove(set)
		}
	}()

	// Load only the clone: its own recipes, held to the same rules as any.
	fresh, err := loadOne(asideDir)
	if err != nil {
		return nil, err
	}
	if len(fresh.byName) == 0 {
		return nil, fmt.Errorf("no recipes (*.yaml) at the top of %s", opts.URL)
	}
	if err := checkAgainstInstalled(opts.Dir, set, fresh); err != nil {
		return nil, err
	}

	if err := install(asideDir, dest, filepath.Join(opts.Dir, ".old-"+set), opts.Replace); err != nil {
		return nil, err
	}
	installed = true
	return fresh.Names(), nil
}

// rename is os.Rename, a variable so a test can make one step fail.
var rename = os.Rename

// install moves a checked set into place. Replacing sets the old one aside
// first and puts it back if the new one cannot be moved in, so a replace that
// fails part way leaves the old set installed, never neither.
func install(from, dest, old string, replace bool) error {
	if !replace {
		return rename(from, dest)
	}
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	_, statErr := os.Lstat(dest)
	hadOld := statErr == nil
	if hadOld {
		if err := rename(dest, old); err != nil {
			return err
		}
	}
	if err := rename(from, dest); err != nil {
		if hadOld {
			if rerr := rename(old, dest); rerr != nil {
				return fmt.Errorf("%w; the previous set is at %s: %v", err, old, rerr)
			}
		}
		return err
	}
	return os.RemoveAll(old)
}

// loadOne loads the recipes directly inside one set directory.
func loadOne(dir string) (*Set, error) {
	s := &Set{byName: make(map[string]Recipe), paths: make(map[string]string)}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		r, err := Parse(f)
		if err != nil {
			return nil, err
		}
		if prev, dup := s.paths[r.Name]; dup {
			return nil, fmt.Errorf("%s: recipe %q already defined in %s", f, r.Name, prev)
		}
		s.byName[r.Name] = r
		s.paths[r.Name] = f
	}
	return s, nil
}

// checkAgainstInstalled refuses fresh if it reuses the name of a recipe in the
// recipes directory outside the set being installed or replaced.
func checkAgainstInstalled(dir, set string, fresh *Set) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var others []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != set {
			others = append(others, filepath.Join(dir, e.Name()))
		}
	}
	for _, d := range others {
		have, err := loadOne(d)
		if err != nil {
			// A broken installed set is not this set's fault; skip it.
			continue
		}
		for _, n := range fresh.Names() {
			if _, dup := have.byName[n]; dup {
				return fmt.Errorf("recipe %q is already defined in %s", n, have.paths[n])
			}
		}
	}
	// Loose recipes directly in the directory count too.
	loose, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range loose {
		stem := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if _, dup := fresh.byName[stem]; dup {
			return fmt.Errorf("recipe %q is already defined in %s", stem, f)
		}
	}
	return nil
}
