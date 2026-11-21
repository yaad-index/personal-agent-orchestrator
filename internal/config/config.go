// Package config loads pao's two configs (ADR-0001 point 4): the orchestrator
// config (this node and where its agents' profiles live) and the per-agent
// profiles. Unknown keys are an error, so a typo is not silently ignored.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
)

// Orchestrator is the node-level config. Sources, channels, routing and
// credentials join it as the milestones that need them land.
type Orchestrator struct {
	// Node names this node.
	Node string `yaml:"node"`
	// ProfilesDir holds one YAML profile per agent. A relative path is
	// resolved against the directory of the orchestrator config.
	ProfilesDir string `yaml:"profiles_dir"`
	// RecipesDir holds recipes for temporary agents (ADR-0006 point 5): *.yaml
	// files directly in it and in each set directory pulled from a git
	// repository. A relative path is resolved against the directory of the
	// orchestrator config. Default: recipes under the state dir.
	RecipesDir string `yaml:"recipes_dir"`
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
	// MeshFile is the mesh document: the list of nodes in the mesh, the same on
	// every node (ADR-0017 point 9). Empty means this node is not part of a mesh.
	// A relative path is resolved against the directory of the orchestrator config.
	MeshFile string `yaml:"mesh_file"`
	// MeshListen is the host:port this node accepts other nodes on. Required when
	// MeshFile is set. Unlike the local channel it is meant to be reached from
	// other machines, so it is not limited to loopback.
	MeshListen string `yaml:"mesh_listen"`
	// MeshKey is this node's private key, created by `pao mesh keygen`. Default:
	// mesh.key in the state dir. A relative path is resolved against the directory
	// of the orchestrator config.
	MeshKey string `yaml:"mesh_key"`
	// Gate configures the approval gate (ADR-0004).
	Gate Gate `yaml:"gate"`
	// Temp configures temporary agents (ADR-0006).
	Temp Temp `yaml:"temp"`
	// Channels are the human fronts to bring up beyond the local channel
	// (ADR-0020 point 6).
	Channels []Channel `yaml:"channels"`
	// MCPUpstreams are the MCP servers pao reaches as a client and offers tools of
	// on its own MCP surface (ADR-0024).
	MCPUpstreams []MCPUpstream `yaml:"mcp_upstreams"`
	// Bindings are the standing bindings: who follows an agent besides the
	// conversation it is answering (ADR-0029 point 3).
	Bindings []Binding `yaml:"bindings"`
}

// Binding subscribes a destination to an agent's stream at a level (ADR-0029
// point 3).
type Binding struct {
	// Agent is the agent followed, by name.
	Agent string `yaml:"agent"`
	// Stream is the stream followed: "stdout", the agent's output, which is the
	// default, or "stderr", pao's gate questions and notices about the agent
	// (ADR-0029 point 4).
	Stream string `yaml:"stream"`
	// To is the destination, by name: group:<name>, private:<subject>,
	// private:<subject>@<channel> or channel:local (ADR-0029 point 1).
	To string `yaml:"to"`
	// Level is the verbosity level the destination is shown the stream at, read
	// against the agent's level map (ADR-0008), or "log": every kind, and the
	// messages delivered to the agent too. Default: the node's level. stdout
	// only: stderr is not shown by level.
	Level BindingLevel `yaml:"level"`
	// Prefix, when set, is put before every message the binding sends, in place
	// of the agent's name (ADR-0029 point 5). At most MaxPrefix characters.
	// stdout only.
	Prefix string `yaml:"prefix"`
}

// BindingLevel is a binding's level: a number, or "log", which is core.LevelLog.
type BindingLevel int

// LevelLog is the word for the highest level, core.LevelLog.
const LevelLog = "log"

// UnmarshalYAML reads a number or "log".
func (l *BindingLevel) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Value == LevelLog {
		*l = BindingLevel(core.LevelLog)
		return nil
	}
	var i int
	if err := n.Decode(&i); err != nil {
		return fmt.Errorf("level must be a number or %q", LevelLog)
	}
	*l = BindingLevel(i)
	return nil
}

// The streams a binding may follow (ADR-0023 point 1).
const (
	// StreamStdout is an agent's output.
	StreamStdout = "stdout"
	// StreamStderr is pao's gate questions and notices about an agent.
	StreamStderr = "stderr"
)

// MaxPrefix is the longest prefix a binding may carry (ADR-0029 point 5).
const MaxPrefix = 10

// The kinds of destination (ADR-0029 point 1).
const (
	DestGroup   = "group"
	DestPrivate = "private"
	DestChannel = "channel"
)

// Destination is a parsed destination name.
type Destination struct {
	// Kind is DestGroup, DestPrivate or DestChannel.
	Kind string
	// Name is the group's name, the subject's name or the channel's name.
	Name string
	// Channel qualifies a private destination by the channel it is on; empty
	// leaves it to the subject's one channel.
	Channel string
}

// String is the destination's name.
func (d Destination) String() string {
	s := d.Kind + ":" + d.Name
	if d.Channel != "" {
		s += "@" + d.Channel
	}
	return s
}

// ParseDestination reads a destination name. It checks the form only: whether the
// group, subject or channel exists is checked where they are known.
func ParseDestination(s string) (Destination, error) {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		return Destination{}, fmt.Errorf("destination %q must be group:<name>, private:<subject>[@<channel>] or channel:<name>", s)
	}
	d := Destination{Kind: kind, Name: rest}
	if kind == DestPrivate {
		d.Name, d.Channel, _ = strings.Cut(rest, "@")
		if strings.Contains(rest, "@") && !channelNamePattern.MatchString(d.Channel) {
			return Destination{}, fmt.Errorf("destination %q: channel %q is not a channel name", s, d.Channel)
		}
	}
	switch kind {
	case DestGroup, DestChannel:
		if !channelNamePattern.MatchString(d.Name) {
			return Destination{}, fmt.Errorf("destination %q: %q is not a %s name", s, d.Name, kind)
		}
	case DestPrivate:
		if !namePattern.MatchString(d.Name) {
			return Destination{}, fmt.Errorf("destination %q: %q is not a subject name", s, d.Name)
		}
	default:
		return Destination{}, fmt.Errorf("destination %q: kind %q must be %s, %s or %s", s, kind, DestGroup, DestPrivate, DestChannel)
	}
	return d, nil
}

// MCPUpstream configures one upstream MCP server (ADR-0024 points 1 and 7).
type MCPUpstream struct {
	// Name is the server's name: it begins every pao-side name of its tools, as
	// "<name>__<tool>", so it holds no underscore.
	Name string `yaml:"name"`
	// URL is the server's streamable HTTP endpoint.
	URL string `yaml:"url"`
	// CredentialEnv names the environment variable holding the bearer token pao
	// presents to the server, if it needs one. The token never lives in a file and
	// is never given to an agent.
	CredentialEnv string `yaml:"credential_env"`
	// Tools are the server's tools pao offers, by the server's own tool name. A
	// tool the server has and this does not name is not offered.
	Tools map[string]UpstreamTool `yaml:"tools"`
}

// UpstreamTool is how pao offers one upstream tool. Both fields are the
// operator's: nothing the upstream server says about its tool decides them.
type UpstreamTool struct {
	// Tags are the tool's tags in the access matrix (ADR-0012 point 3).
	Tags []string `yaml:"tags"`
	// Risk is the risk level the approval gate reads a call of the tool at
	// (ADR-0004 point 2). Empty leaves it unclassified, so every call is asked.
	Risk gate.Risk `yaml:"risk"`
}

// MaxToolName is the longest pao-side tool name: the limit MCP clients hold a tool
// name to.
const MaxToolName = 64

// ToolName is the pao-side name of an upstream server's tool.
func ToolName(upstream, tool string) string { return upstream + "__" + tool }

// Channel configures one channel adapter (ADR-0020 point 6, ADR-0021 points 1
// and 2).
type Channel struct {
	// ID is the channel's numeric id, positive and unique, assigned once and never
	// reused: it names the channel in every composite message id.
	ID int `yaml:"id"`
	// Name is the channel's name, the namespace of its bindings.
	Name string `yaml:"name"`
	// Kind selects an adapter compiled into this build.
	Kind string `yaml:"kind"`
	// CredentialEnv names the environment variable that holds the channel's
	// credential. The credential itself never lives in a config file.
	CredentialEnv string `yaml:"credential_env"`
	// Options are the adapter's own settings; only the adapter interprets them.
	Options map[string]any `yaml:"options"`
	// Groups are the group conversations the channel serves (ADR-0028 point 1).
	// A group not listed here is refused. If the platform moves a conversation to
	// a new id, the new id is not listed and the group is refused until it is.
	Groups []Group `yaml:"groups"`
}

// MembersOperators is the member policy that admits only subjects routed as the
// operator (ADR-0028 point 2). It is the only policy there is.
const MembersOperators = "operators"

// Group admits one group conversation on a channel (ADR-0028 points 1 and 2).
type Group struct {
	// ID is the platform's own id for the conversation, the one a reply to it
	// goes to. It is what the group is admitted and delivered by.
	ID string `yaml:"id"`
	// Name is the operator's handle for the group: lowercase letters, digits,
	// '-' or '_', unique among the node's groups. It names the group wherever pao
	// shows it to the operator, such as the record of rejected inbound.
	Name string `yaml:"name"`
	// Members is the group's member policy: who may act in it.
	Members string `yaml:"members"`
}

var (
	upstreamNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	toolNamePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	channelNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	envNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// reservedChannelNames are names the node itself uses for a channel.
var reservedChannelNames = map[string]bool{"local": true}

func checkChannels(path string, chs []Channel) error {
	ids, names, groupNames := map[int]string{}, map[string]bool{}, map[string]string{}
	for i, c := range chs {
		at := fmt.Sprintf("%s: channels[%d]", path, i)
		switch {
		case c.ID < 1:
			return fmt.Errorf("%s: id must be a positive integer", at)
		case !channelNamePattern.MatchString(c.Name):
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, '-' or '_', at most 32", at, c.Name)
		case reservedChannelNames[c.Name]:
			return fmt.Errorf("%s: name %q is reserved", at, c.Name)
		case c.Kind == "":
			return fmt.Errorf("%s: kind is required", at)
		case !envNamePattern.MatchString(c.CredentialEnv):
			return fmt.Errorf("%s: credential_env must name an environment variable", at)
		}
		if prev, dup := ids[c.ID]; dup {
			return fmt.Errorf("%s: id %d is already channel %q", at, c.ID, prev)
		}
		if names[c.Name] {
			return fmt.Errorf("%s: name %q is already used", at, c.Name)
		}
		ids[c.ID], names[c.Name] = c.Name, true
		if err := checkGroups(at, c.Groups, groupNames); err != nil {
			return err
		}
	}
	return nil
}

// checkGroups validates one channel's groups. names holds the group names already
// used on the node, by where they were used; a group's name is added to it.
func checkGroups(at string, gs []Group, names map[string]string) error {
	seen := map[string]bool{}
	for i, g := range gs {
		gat := fmt.Sprintf("%s.groups[%d]", at, i)
		switch {
		case strings.TrimSpace(g.ID) == "" || g.ID != strings.TrimSpace(g.ID):
			return fmt.Errorf("%s: id is required, with no surrounding space", gat)
		case !channelNamePattern.MatchString(g.Name):
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, '-' or '_', at most 32", gat, g.Name)
		case g.Members != MembersOperators:
			return fmt.Errorf("%s: members must be %q", gat, MembersOperators)
		case seen[g.ID]:
			return fmt.Errorf("%s: id %q is already listed", gat, g.ID)
		}
		if prev, dup := names[g.Name]; dup {
			return fmt.Errorf("%s: name %q is already %s", gat, g.Name, prev)
		}
		seen[g.ID], names[g.Name] = true, gat
	}
	return nil
}

func checkUpstreams(path string, us []MCPUpstream) error {
	names := map[string]bool{}
	for i, u := range us {
		at := fmt.Sprintf("%s: mcp_upstreams[%d]", path, i)
		if !upstreamNamePattern.MatchString(u.Name) {
			return fmt.Errorf("%s: name %q must be lowercase letters, digits or '-', at most 32", at, u.Name)
		}
		if names[u.Name] {
			return fmt.Errorf("%s: name %q is already used", at, u.Name)
		}
		names[u.Name] = true
		if pu, err := url.Parse(u.URL); err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return fmt.Errorf("%s: url must be an http or https URL", at)
		}
		if u.CredentialEnv != "" && !envNamePattern.MatchString(u.CredentialEnv) {
			return fmt.Errorf("%s: credential_env must name an environment variable", at)
		}
		if len(u.Tools) == 0 {
			return fmt.Errorf("%s: tools must name at least one of the server's tools", at)
		}
		for tool, t := range u.Tools {
			if !toolNamePattern.MatchString(tool) || len(ToolName(u.Name, tool)) > MaxToolName {
				return fmt.Errorf("%s: tool %q must be letters, digits, '_' or '-', and %q at most %d long", at, tool, ToolName(u.Name, tool), MaxToolName)
			}
			if t.Risk != "" && !t.Risk.Valid() {
				return fmt.Errorf("%s: tool %q: unknown risk %q", at, tool, t.Risk)
			}
			for _, tag := range t.Tags {
				if tag == "" || strings.ContainsAny(tag, " \t\n") {
					return fmt.Errorf("%s: tool %q has an empty tag or one with spaces", at, tool)
				}
			}
		}
	}
	return nil
}

// Temp is the temporary-agent settings.
type Temp struct {
	// UnusedAfter is how long after spawning an agent that never received a
	// message is flagged unused. Default 10m.
	UnusedAfter time.Duration `yaml:"unused_after"`
	// IdleAfter is how long a used agent may be quiet before it is flagged
	// idle. Default 30m. The flags only show; nothing is killed on them.
	IdleAfter time.Duration `yaml:"idle_after"`
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

// Defaults for when a temporary agent is flagged.
const (
	DefaultUnusedAfter = 10 * time.Minute
	DefaultIdleAfter   = 30 * time.Minute
)

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
	// Mesh is the mesh document, nil when the node is not part of a mesh.
	Mesh *mesh.Document
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Load reads the orchestrator config at path and every profile in its
// profiles directory.
func Load(path string) (*Config, error) { return load(path, true) }

// LoadWithoutMesh is Load without reading or checking the mesh document. It is
// for the command that creates this node's key, which has to run before the node
// can be listed in a mesh document that names its key.
func LoadWithoutMesh(path string) (*Config, error) { return load(path, false) }

func load(path string, withMesh bool) (*Config, error) {
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
	if o.Temp.UnusedAfter == 0 {
		o.Temp.UnusedAfter = DefaultUnusedAfter
	}
	if o.Temp.IdleAfter == 0 {
		o.Temp.IdleAfter = DefaultIdleAfter
	}
	if o.Temp.UnusedAfter < 0 || o.Temp.IdleAfter < 0 {
		return nil, fmt.Errorf("%s: temp.unused_after and temp.idle_after must be positive", path)
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
	if o.RecipesDir == "" {
		o.RecipesDir = filepath.Join(o.StateDir, "recipes")
	} else if !filepath.IsAbs(o.RecipesDir) {
		o.RecipesDir = filepath.Join(filepath.Dir(path), o.RecipesDir)
	}
	if o.MeshKey == "" {
		o.MeshKey = filepath.Join(o.StateDir, "mesh.key")
	} else if !filepath.IsAbs(o.MeshKey) {
		o.MeshKey = filepath.Join(filepath.Dir(path), o.MeshKey)
	}
	if err := checkChannels(path, o.Channels); err != nil {
		return nil, err
	}
	if err := checkUpstreams(path, o.MCPUpstreams); err != nil {
		return nil, err
	}
	var meshDoc *mesh.Document
	if o.MeshFile != "" && !withMesh {
		if !filepath.IsAbs(o.MeshFile) {
			o.MeshFile = filepath.Join(filepath.Dir(path), o.MeshFile)
		}
	} else if o.MeshFile != "" {
		if !filepath.IsAbs(o.MeshFile) {
			o.MeshFile = filepath.Join(filepath.Dir(path), o.MeshFile)
		}
		if o.MeshListen == "" {
			return nil, fmt.Errorf("%s: mesh_listen is required when mesh_file is set", path)
		}
		if _, _, err := net.SplitHostPort(o.MeshListen); err != nil {
			return nil, fmt.Errorf("%s: mesh_listen %q must be host:port", path, o.MeshListen)
		}
		var err error
		if meshDoc, err = mesh.LoadDocument(o.MeshFile); err != nil {
			return nil, err
		}
		if _, ok := meshDoc.Entry(o.Node); !ok {
			return nil, fmt.Errorf("%s: this node (%q) is not in the mesh document %s", path, o.Node, o.MeshFile)
		}
	} else if o.MeshListen != "" {
		return nil, fmt.Errorf("%s: mesh_listen is set but mesh_file is not", path)
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

	cfg := &Config{Orchestrator: o, Mesh: meshDoc}
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
	if err := checkBindings(path, &cfg.Orchestrator, seen); err != nil {
		return nil, err
	}
	return cfg, nil
}

// checkBindings validates the standing bindings against the node's agents,
// channels and groups, and fills in the defaults. Whether a private destination's
// subject exists is known only at run time.
func checkBindings(path string, o *Orchestrator, agents map[string]string) error {
	groups, channels := map[string]bool{}, map[string]bool{}
	for _, c := range o.Channels {
		channels[c.Name] = true
		for _, g := range c.Groups {
			groups[g.Name] = true
		}
	}
	seen := map[string]int{}
	for i := range o.Bindings {
		b := &o.Bindings[i]
		at := fmt.Sprintf("%s: bindings[%d]", path, i)
		if b.Stream == "" {
			b.Stream = StreamStdout
		}
		stderrSet := b.Stream == StreamStderr && (b.Level != 0 || b.Prefix != "")
		if b.Level == 0 && b.Stream == StreamStdout {
			b.Level = BindingLevel(o.Level)
		}
		d, err := ParseDestination(b.To)
		switch {
		case agents[b.Agent] == "":
			return fmt.Errorf("%s: agent %q is not a configured agent", at, b.Agent)
		case b.Stream != StreamStdout && b.Stream != StreamStderr:
			return fmt.Errorf("%s: stream must be %q or %q", at, StreamStdout, StreamStderr)
		case stderrSet:
			return fmt.Errorf("%s: a %s binding takes no level and no prefix", at, StreamStderr)
		case err != nil:
			return fmt.Errorf("%s: %w", at, err)
		case d.Kind == DestGroup && !groups[d.Name]:
			return fmt.Errorf("%s: no admitted group is named %q", at, d.Name)
		case d.Kind == DestPrivate && d.Channel != "" && !channels[d.Channel]:
			return fmt.Errorf("%s: no channel is named %q", at, d.Channel)
		case d.Kind == DestChannel && !reservedChannelNames[d.Name]:
			return fmt.Errorf("%s: channel %q has several conversations: name one as %s:<name> or %s:<subject>", at, d.Name, DestGroup, DestPrivate)
		case b.Stream == StreamStdout && b.Level < 1:
			return fmt.Errorf("%s: level must be at least 1", at)
		case utf8.RuneCountInString(b.Prefix) > MaxPrefix:
			return fmt.Errorf("%s: prefix is longer than %d characters", at, MaxPrefix)
		case strings.ContainsAny(b.Prefix, "\r\n"):
			return fmt.Errorf("%s: prefix is one line", at)
		}
		key := b.Agent + "\x00" + b.Stream + "\x00" + d.String()
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%s: agent %q is already bound to %s at bindings[%d]", at, b.Agent, d, prev)
		}
		seen[key] = i
	}
	return nil
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

// ValidateProfile reports whether p is a complete, valid agent profile.
func ValidateProfile(p core.Profile) error { return validate(p) }

func validate(p core.Profile) error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits, '-' or '_'", p.Name)
	}
	if slices.Contains(core.ReservedNames, p.Name) {
		return fmt.Errorf("name %q is reserved: pao speaks as it", p.Name)
	}
	if slices.Contains(core.CommandWords, p.Name) {
		return fmt.Errorf("name %q is reserved: it is a pao command word", p.Name)
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
	return builtin.Table().Check(p)
}

// DecodeFile decodes the YAML file at path into into. Unknown keys are an
// error, so a typo is not silently ignored.
func DecodeFile(path string, into any) error { return decodeFile(path, into) }

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
