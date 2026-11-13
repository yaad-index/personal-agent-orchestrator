// Package recipe loads the recipes temporary agents are spawned from
// (ADR-0006 point 5). A recipe is data: a profile, a workdir source and a tool
// set, in a YAML file outside pao's code. Sets of recipes are pulled from a git
// repository into the recipes directory, so adding one never changes pao.
//
// A recipe's startup command comes from whoever wrote it and is run in a
// terminal when the agent spawns, exactly like a profile's. Pull recipes only
// from a repository you trust.
package recipe

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

var braced = regexp.MustCompile(`\{([a-z_]+)\}`)

// placeholders finds the {word} placeholders in s as [start, end, nameStart,
// nameEnd]. A ${word} is a shell variable and is not one.
func placeholders(s string) [][]int {
	var out [][]int
	for _, m := range braced.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > 0 && s[m[0]-1] == '$' {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Recipe is one recipe file.
type Recipe struct {
	// Name is the recipe's name; it must equal the file's name without .yaml.
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Profile is the ephemeral profile. Its name and workdir are set per spawn
	// and must be left out. Its startup command and transcript path may use
	// the placeholders {name}, {workdir} and {tools}; an empty transcript path
	// is derived from the workdir by the backend.
	Profile core.Profile `yaml:"profile"`
	// Workdir says where the agent's isolated workdir comes from.
	Workdir Workdir `yaml:"workdir"`
	// Tools is the tool set the agent may use. It reaches the startup command
	// as {tools}, comma-separated and shell-quoted; whether the backend honours
	// it is the startup command's business, and the gate (ADR-0004) still
	// decides every action.
	Tools []string `yaml:"tools"`
}

// Workdir is a recipe's workdir source: exactly one of Git and Files.
type Workdir struct {
	Git *Git `yaml:"git"`
	// Files are pushed files, path to content.
	Files map[string]string `yaml:"files"`
}

// Git is a git clone source.
type Git struct {
	URL string `yaml:"url"`
	Ref string `yaml:"ref"`
}

// Source returns the workdir source in the form the provisioner takes.
func (r Recipe) Source() workdir.Source {
	var src workdir.Source
	if r.Workdir.Git != nil {
		src.Git = &workdir.Git{URL: r.Workdir.Git.URL, Ref: r.Workdir.Git.Ref}
	}
	if r.Workdir.Files != nil {
		src.Files = make(map[string][]byte, len(r.Workdir.Files))
		for k, v := range r.Workdir.Files {
			src.Files[k] = []byte(v)
		}
	}
	return src
}

// Locator says where a backend writes the transcripts of sessions started in
// workdir, for a transcript format; ok is false for a format it does not know.
type Locator func(format, workdir string) (path string, ok bool)

// Instantiate returns the profile of an agent named name, running in workdir.
// The result is a complete profile that passes the same checks as one in the
// profiles directory.
func (r Recipe) Instantiate(name, workdir string, locate Locator) (core.Profile, error) {
	p := r.Profile
	// The recipe is shared by every spawn from it: nothing a profile holds by
	// reference may point back into it.
	p.Tags = slices.Clone(r.Profile.Tags)
	p.Levels = maps.Clone(r.Profile.Levels)
	p.Name = name
	p.Workdir = workdir

	cmdVars := map[string]string{"name": shellQuote(name), "workdir": shellQuote(workdir), "tools": shellQuote(strings.Join(r.Tools, ","))}
	rawVars := map[string]string{"name": name, "workdir": workdir, "tools": strings.Join(r.Tools, ",")}
	p.Startup.Command = substitute(p.Startup.Command, cmdVars)
	p.Transcript.Path = substitute(p.Transcript.Path, rawVars)
	if p.Transcript.Path == "" && locate != nil {
		if path, ok := locate(p.Transcript.Format, workdir); ok {
			p.Transcript.Path = path
		}
	}
	if p.Transcript.Path == "" {
		return core.Profile{}, fmt.Errorf("recipe %q: no transcript.path, and the backend cannot locate one for format %q", r.Name, p.Transcript.Format)
	}
	if err := config.ValidateProfile(p); err != nil {
		return core.Profile{}, fmt.Errorf("recipe %q: profile: %w", r.Name, err)
	}
	return p, nil
}

// Check reports whether the recipe is well formed. It does not touch the file
// system or the network.
func (r Recipe) Check() error {
	if !namePattern.MatchString(r.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits, '-' or '_'", r.Name)
	}
	if r.Profile.Name != "" {
		return fmt.Errorf("profile.name must be left out: the name is chosen at spawn")
	}
	if r.Profile.Workdir != "" {
		return fmt.Errorf("profile.workdir must be left out: the workdir is provisioned at spawn")
	}
	for _, s := range []string{r.Profile.Startup.Command, r.Profile.Transcript.Path} {
		for _, m := range placeholders(s) {
			switch word := s[m[2]:m[3]]; word {
			case "name", "workdir", "tools":
			default:
				return fmt.Errorf("unknown placeholder {%s} (known: {name}, {workdir}, {tools})", word)
			}
		}
	}
	for _, t := range r.Tools {
		if t == "" || strings.ContainsAny(t, ",\n\r\x00") {
			return fmt.Errorf("tool %q must be non-empty with no comma or line break", t)
		}
	}
	if err := workdir.Validate(r.Source()); err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	// Instantiate against stand-ins, so the profile is held to the same rules
	// here as at spawn, and a bad recipe fails when it is loaded.
	_, err := r.Instantiate("check", "/check", func(string, string) (string, bool) { return "/check/transcript", true })
	return err
}

func substitute(s string, vars map[string]string) string {
	var b strings.Builder
	last := 0
	for _, m := range placeholders(s) {
		v, ok := vars[s[m[2]:m[3]]]
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(v)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// shellQuote quotes s as one word for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Parse reads and checks the recipe file at path.
func Parse(path string) (Recipe, error) {
	var r Recipe
	if err := config.DecodeFile(path, &r); err != nil {
		return Recipe{}, err
	}
	if stem := strings.TrimSuffix(filepath.Base(path), ".yaml"); r.Name != stem {
		return Recipe{}, fmt.Errorf("%s: name %q must match the file name %q", path, r.Name, stem)
	}
	if err := r.Check(); err != nil {
		return Recipe{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Set is the recipes found under one directory.
type Set struct {
	byName map[string]Recipe
	paths  map[string]string
}

// Get returns the named recipe.
func (s *Set) Get(name string) (Recipe, bool) {
	r, ok := s.byName[name]
	return r, ok
}

// Names returns every recipe name, sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Path returns the file a recipe was loaded from.
func (s *Set) Path(name string) string { return s.paths[name] }

// LoadDir loads every recipe in dir: *.yaml files directly in it, and in each
// directory in it (a set pulled from git). Directories whose name starts with
// '.' are skipped. A directory that does not exist holds no recipes. One bad
// file fails the whole load, and so does a name used twice, so a broken or
// ambiguous recipe is never silently dropped.
func LoadDir(dir string) (*Set, error) {
	s := &Set{byName: make(map[string]Recipe), paths: make(map[string]string)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		switch {
		case e.IsDir() && !strings.HasPrefix(e.Name(), "."):
			sub, err := filepath.Glob(filepath.Join(dir, e.Name(), "*.yaml"))
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml"):
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
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
