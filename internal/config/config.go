// Package config loads pao's two configs (ADR-0001 point 4): the orchestrator
// config (this node and where its agents' profiles live) and the per-agent
// profiles. Unknown keys are an error, so a typo is not silently ignored.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// Orchestrator is the node-level config. Sources, channels, routing and
// credentials join it as the milestones that need them land.
type Orchestrator struct {
	// Node names this node.
	Node string `yaml:"node"`
	// ProfilesDir holds one YAML profile per agent. A relative path is
	// resolved against the directory of the orchestrator config.
	ProfilesDir string `yaml:"profiles_dir"`
	// StateDir holds pao's own state (the message store). A relative path is
	// resolved against the directory of the orchestrator config.
	StateDir string `yaml:"state_dir"`
	// Listen is the address of the local channel. It must be a loopback
	// address; exposing it further is the operator's own choice and is not
	// supported yet. Default 127.0.0.1:7744.
	Listen string `yaml:"listen"`
	// TokenEnv names the environment variable that holds the channel's bearer
	// token. The token itself never lives in a config file.
	TokenEnv string `yaml:"token_env"`
	// TmuxSocket names pao's tmux server socket. Default "pao".
	TmuxSocket string `yaml:"tmux_socket"`
	// MessageWindow is how many recent messages the id map keeps. Default 500.
	MessageWindow int `yaml:"message_window"`
	// Level is the verbosity level agent output is mirrored to the human at
	// (ADR-0008). Default 1.
	Level int `yaml:"level"`
	// Gate configures the approval gate (ADR-0004).
	Gate Gate `yaml:"gate"`
}

// Gate is the approval gate's settings.
type Gate struct {
	// AutoAllow lists the risk levels that need no question. Default [read].
	AutoAllow []string `yaml:"auto_allow"`
	// AskTimeout bounds how long the operator is waited for; an unanswered
	// question is a denial. Default 5m.
	AskTimeout time.Duration `yaml:"ask_timeout"`
}

// DefaultAskTimeout is the gate's default time to wait for the operator.
const DefaultAskTimeout = 5 * time.Minute

// Defaults for the optional orchestrator settings.
const (
	DefaultListen        = "127.0.0.1:7744"
	DefaultTmuxSocket    = "pao"
	DefaultMessageWindow = 500
	DefaultLevel         = 1
)

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
	if o.StateDir == "" {
		return nil, fmt.Errorf("%s: state_dir is required", path)
	}
	if o.TokenEnv == "" {
		return nil, fmt.Errorf("%s: token_env is required", path)
	}
	if o.Listen == "" {
		o.Listen = DefaultListen
	}
	if err := checkLoopback(o.Listen); err != nil {
		return nil, fmt.Errorf("%s: listen: %w", path, err)
	}
	if o.TmuxSocket == "" {
		o.TmuxSocket = DefaultTmuxSocket
	}
	if o.MessageWindow == 0 {
		o.MessageWindow = DefaultMessageWindow
	}
	if o.MessageWindow < 1 {
		return nil, fmt.Errorf("%s: message_window must be at least 1", path)
	}
	if o.Level == 0 {
		o.Level = DefaultLevel
	}
	if o.Level < 1 {
		return nil, fmt.Errorf("%s: level must be at least 1", path)
	}
	if len(o.Gate.AutoAllow) == 0 {
		o.Gate.AutoAllow = []string{"read"}
	}
	for _, r := range o.Gate.AutoAllow {
		if !gate.Risk(r).Valid() {
			return nil, fmt.Errorf("%s: gate.auto_allow: unknown risk %q", path, r)
		}
	}
	if o.Gate.AskTimeout == 0 {
		o.Gate.AskTimeout = DefaultAskTimeout
	}
	if o.Gate.AskTimeout < 0 {
		return nil, fmt.Errorf("%s: gate.ask_timeout must be positive", path)
	}
	if !filepath.IsAbs(o.StateDir) {
		o.StateDir = filepath.Join(filepath.Dir(path), o.StateDir)
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

// checkLoopback accepts only a host:port whose host is a loopback address.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q is not a loopback address", host)
	}
	return nil
}

func validate(p core.Profile) error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits, '-' or '_'", p.Name)
	}
	if p.Backend == "" {
		return errors.New("backend is required")
	}
	if p.Startup.Command == "" {
		return errors.New("startup.command is required")
	}
	if p.Startup.ReadyPattern == "" {
		return errors.New("startup.ready_pattern is required")
	}
	if _, err := regexp.Compile(p.Startup.ReadyPattern); err != nil {
		return fmt.Errorf("startup.ready_pattern: %w", err)
	}
	if p.Startup.ReadyTimeout <= 0 {
		return errors.New("startup.ready_timeout must be positive")
	}
	if p.Transcript.Format == "" {
		return errors.New("transcript.format is required")
	}
	if p.Transcript.Path == "" {
		return errors.New("transcript.path is required")
	}
	if err := p.Levels.Validate(); err != nil {
		return fmt.Errorf("levels: %w", err)
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
