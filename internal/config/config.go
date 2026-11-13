// Package config loads pao's two configs (ADR-0001 point 4): the orchestrator
// config (this node and where its agents' profiles live) and the per-agent
// profiles. Unknown keys are an error, so a typo is not silently ignored.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Orchestrator is the node-level config. Sources, channels, routing and
// credentials join it as the milestones that need them land.
type Orchestrator struct {
	// Node names this node.
	Node string `yaml:"node"`
	// ProfilesDir holds one YAML profile per agent. A relative path is
	// resolved against the directory of the orchestrator config.
	ProfilesDir string `yaml:"profiles_dir"`
}

// Config is the loaded and validated result.
type Config struct {
	Orchestrator Orchestrator
	Profiles     []core.Profile
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Load reads the orchestrator config at path and every profile in its
// profiles directory.
func Load(path string) (*Config, error) {
	var o Orchestrator
	if err := decodeFile(path, &o); err != nil {
		return nil, err
	}
	if o.Node == "" {
		return nil, fmt.Errorf("%s: node is required", path)
	}
	if o.ProfilesDir == "" {
		return nil, fmt.Errorf("%s: profiles_dir is required", path)
	}
	dir := o.ProfilesDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(path), dir)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	cfg := &Config{Orchestrator: o}
	seen := make(map[string]string)
	for _, f := range files {
		var p core.Profile
		if err := decodeFile(f, &p); err != nil {
			return nil, err
		}
		if err := validate(p); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if prev, dup := seen[p.Name]; dup {
			return nil, fmt.Errorf("%s: agent name %q already defined in %s", f, p.Name, prev)
		}
		seen[p.Name] = f
		cfg.Profiles = append(cfg.Profiles, p)
	}
	return cfg, nil
}

func validate(p core.Profile) error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits, '-' or '_'", p.Name)
	}
	if p.Backend == "" {
		return errors.New("backend is required")
	}
	for _, t := range p.Tags {
		if !namePattern.MatchString(t) {
			return fmt.Errorf("tag %q must be lowercase letters, digits, '-' or '_'", t)
		}
	}
	return nil
}

func decodeFile(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s: %s", path, strings.TrimSpace(err.Error()))
	}
	return nil
}
