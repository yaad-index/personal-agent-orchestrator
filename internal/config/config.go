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
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress"
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
	// ProfileDefaults apply to every agent profile that does not say otherwise,
	// a temporary agent's included (#179).
	ProfileDefaults ProfileDefaults `yaml:"profile_defaults"`
	// Channels are the human fronts to bring up beyond the local channel
	// (ADR-0020 point 6).
	Channels []Channel `yaml:"channels"`
	// MCPUpstreams are the MCP servers pao reaches as a client and offers tools of
	// on its own MCP surface (ADR-0024).
	MCPUpstreams []MCPUpstream `yaml:"mcp_upstreams"`
	// Bindings are the standing bindings: who follows an agent besides the
	// conversation it is answering (ADR-0029 point 3).
	Bindings []Binding `yaml:"bindings"`
	// Governor names the node's governor (ADR-0030), the agent /gpao requests
	// go to. Unset, the node has none.
	Governor Governor `yaml:"governor"`
	// Accounts are the accounts the node's agents draw on (ADR-0015 point 3,
	// ADR-0035). A profile names one by its name.
	Accounts []Account `yaml:"accounts"`
	// Budget is how the accounts' figures are kept read.
	Budget Budget `yaml:"budget"`
	// Secrets are the secrets a task may name, each held orchestrator-side and
	// resolved only at run (ADR-0036 point 3, extending ADR-0015 point 4).
	Secrets []Secret `yaml:"secrets"`
}

// Secret is one secret a task may name. Its value is in the daemon's
// environment or in a file, never in this config.
type Secret struct {
	// Name is how a task names it.
	Name string `yaml:"name"`
	// Env names the variable in the daemon's environment that holds it.
	Env string `yaml:"env"`
	// File is the path of a file that holds it. A relative path is resolved
	// against the config file's directory.
	File string `yaml:"file"`
	// Inject, when set, puts the secret into an agent's calls through the
	// egress proxy instead of giving the agent its value (ADR-0049). The hosts
	// it may go to are set here, by the operator, never by the task that names
	// the secret: a task can be written by an agent.
	Inject *SecretInject `yaml:"inject"`
}

// SecretInject is where an injected secret may go and how the agent names it.
type SecretInject struct {
	// Hosts are the exact hosts the value is filled in for.
	Hosts []string `yaml:"hosts"`
	// Env is the variable the agent is given the placeholder in.
	Env string `yaml:"env"`
}

// Secret returns the secret named name.
func (o Orchestrator) Secret(name string) (Secret, bool) {
	for _, s := range o.Secrets {
		if s.Name == name {
			return s, true
		}
	}
	return Secret{}, false
}

// Account is one account agents draw on. Whether it is metered is a
// declaration that must be made (ADR-0035 point 2): an account that leaves it
// out is refused, so an account is never unmetered by omission.
type Account struct {
	// Name is how a profile names the account.
	Name string `yaml:"name"`
	// Metered says the account has a budget pao reads. False declares it
	// unmetered: its agents are never held back for budget.
	Metered *bool `yaml:"metered"`
	// Reader names how a metered account's figure is read. It is required on a
	// metered account and refused on an unmetered one.
	Reader string `yaml:"reader"`
	// Credentials is the path of the file the reader reads the account's
	// credential from, kept orchestrator-side (ADR-0015 point 4). For the
	// claude-subscription reader it is the harness's credentials file for the
	// login. A relative path is resolved against the config file's directory.
	Credentials string `yaml:"credentials"`
}

// Budget is how accounts' figures are kept read.
type Budget struct {
	// Poll is how often each metered account is read. Default 5m. How an
	// agent's headroom is judged from the figure is the agent's own, in its
	// profile's budget.
	Poll time.Duration `yaml:"poll"`
	// MaxStale is how old the last good figure may grow while reads fail for a
	// reason that passes (a rate limit, a server error, no connection) before
	// the account is at fault. Default 15m; never less than Poll.
	MaxStale time.Duration `yaml:"max_stale"`
}

// The budget defaults.
const (
	DefaultBudgetPoll     = 5 * time.Minute
	DefaultBudgetMaxStale = 15 * time.Minute
)

// ReaderClaudeSubscription reads a subscription account of the coding-agent
// harness: the share left of its weekly and five-hour windows.
const ReaderClaudeSubscription = "claude-subscription"

// Readers are the account readers this build knows.
var Readers = []string{ReaderClaudeSubscription}

// Account returns the account named name.
func (o Orchestrator) Account(name string) (Account, bool) {
	for _, a := range o.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return Account{}, false
}

// CheckAccount reports whether a profile's account is one this node has. A
// profile that names none passes: its agent has no readable budget, which the
// budget reader treats as a fault, never as unmetered (ADR-0035 point 3).
func (o Orchestrator) CheckAccount(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := o.Account(name); !ok {
		return fmt.Errorf("account %q is not a configured account", name)
	}
	return nil
}

// Governor configures the node's governor: one agent, shared by the node's
// operators, that turns a /gpao request into a command it proposes.
type Governor struct {
	// Agent is the governor, by the name of a configured agent. Empty: the node
	// has no governor, and a /gpao request is answered so.
	Agent string `yaml:"agent"`
	// Propose narrows the commands the governor may propose to these command
	// words. Empty: every command a governor may propose.
	Propose []string `yaml:"propose"`
	// Prefix is the first word of a request to the governor. Default "/gpao".
	Prefix string `yaml:"prefix"`
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
	// Topics names some of the group's topics, on a platform that splits a group
	// into topics (ADR-0038 point 10). Every topic of an admitted group is served
	// whether it is named or not; a named one is also a destination, group:<name>.
	Topics []Topic `yaml:"topics"`
}

// Topic names one topic of an admitted group (ADR-0038 point 10). It has no
// member policy of its own: its readers are its group's.
type Topic struct {
	// ID is the platform's own id for the topic within its group.
	ID string `yaml:"id"`
	// Name is the operator's handle for the topic, unique among the node's
	// groups and topics, in the same form as a group's name.
	Name string `yaml:"name"`
}

// Names returns the names the group gives the node's destinations: its own, and
// each of its named topics'.
func (g Group) Names() []string {
	out := []string{g.Name}
	for _, t := range g.Topics {
		out = append(out, t.Name)
	}
	return out
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
		case len(g.Topics) > 0 && strings.Contains(g.ID, "/"):
			return fmt.Errorf("%s: id %q of a group with topics must not contain '/'", gat, g.ID)
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
		if err := checkTopics(gat, g.Topics, names); err != nil {
			return err
		}
	}
	return nil
}

// checkTopics validates one group's named topics. names holds the group and topic
// names already used on the node; a topic's name is added to it.
func checkTopics(at string, ts []Topic, names map[string]string) error {
	seen := map[string]bool{}
	for i, t := range ts {
		tat := fmt.Sprintf("%s.topics[%d]", at, i)
		switch {
		case strings.TrimSpace(t.ID) == "" || t.ID != strings.TrimSpace(t.ID) || strings.Contains(t.ID, "/"):
			return fmt.Errorf("%s: id is required, with no surrounding space and no '/'", tat)
		case !channelNamePattern.MatchString(t.Name):
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, '-' or '_', at most 32", tat, t.Name)
		case seen[t.ID]:
			return fmt.Errorf("%s: id %q is already listed", tat, t.ID)
		}
		if prev, dup := names[t.Name]; dup {
			return fmt.Errorf("%s: name %q is already %s", tat, t.Name, prev)
		}
		seen[t.ID], names[t.Name] = true, tat
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

func checkAccounts(path string, as []Account) error {
	names := map[string]bool{}
	for i, a := range as {
		at := fmt.Sprintf("%s: accounts[%d]", path, i)
		if !namePattern.MatchString(a.Name) {
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, '-' or '_'", at, a.Name)
		}
		if names[a.Name] {
			return fmt.Errorf("%s: name %q is already used", at, a.Name)
		}
		names[a.Name] = true
		switch {
		case a.Metered == nil:
			return fmt.Errorf("%s: metered must be set: true for an account with a budget, false to declare it unmetered", at)
		case !*a.Metered && (a.Reader != "" || a.Credentials != ""):
			return fmt.Errorf("%s: reader and credentials are for a metered account, and this one is unmetered", at)
		case *a.Metered && !slices.Contains(Readers, a.Reader):
			return fmt.Errorf("%s: reader %q is not one of %s", at, a.Reader, strings.Join(Readers, ", "))
		case a.Reader == ReaderClaudeSubscription && a.Credentials == "":
			return fmt.Errorf("%s: the %s reader needs credentials, the path of the login's credentials file", at, a.Reader)
		}
	}
	return nil
}

func checkSecrets(path string, ss []Secret) error {
	names := map[string]bool{}
	for i, s := range ss {
		at := fmt.Sprintf("%s: secrets[%d]", path, i)
		if !namePattern.MatchString(s.Name) {
			return fmt.Errorf("%s: name %q must be lowercase letters, digits, '-' or '_'", at, s.Name)
		}
		if names[s.Name] {
			return fmt.Errorf("%s: name %q is already used", at, s.Name)
		}
		names[s.Name] = true
		switch {
		case (s.Env == "") == (s.File == ""):
			return fmt.Errorf("%s: give exactly one of env and file", at)
		case s.Env != "" && !envNamePattern.MatchString(s.Env):
			return fmt.Errorf("%s: env must name an environment variable", at)
		}
		if in := s.Inject; in != nil {
			if len(in.Hosts) == 0 {
				return fmt.Errorf("%s: inject.hosts names no host", at)
			}
			for _, h := range in.Hosts {
				if err := egress.CheckHost(h); err != nil {
					return fmt.Errorf("%s: inject.hosts: %w", at, err)
				}
			}
			if err := egress.CheckEnv(in.Env); err != nil {
				return fmt.Errorf("%s: inject.env: %w", at, err)
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
	if err := checkTools(o.ProfileDefaults.Tools); err != nil {
		return nil, fmt.Errorf("%s: profile_defaults.tools: %w", path, err)
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
	if err := checkAccounts(path, o.Accounts); err != nil {
		return nil, err
	}
	for i := range o.Accounts {
		if c := o.Accounts[i].Credentials; c != "" && !filepath.IsAbs(c) {
			o.Accounts[i].Credentials = filepath.Join(filepath.Dir(path), c)
		}
	}
	if err := checkSecrets(path, o.Secrets); err != nil {
		return nil, err
	}
	for i := range o.Secrets {
		if f := o.Secrets[i].File; f != "" && !filepath.IsAbs(f) {
			o.Secrets[i].File = filepath.Join(filepath.Dir(path), f)
		}
	}
	if o.Budget.Poll == 0 {
		o.Budget.Poll = DefaultBudgetPoll
	}
	if o.Budget.Poll < 0 {
		return nil, fmt.Errorf("%s: budget.poll must be positive", path)
	}
	if o.Budget.MaxStale == 0 {
		o.Budget.MaxStale = max(DefaultBudgetMaxStale, o.Budget.Poll)
	}
	if o.Budget.MaxStale < o.Budget.Poll {
		return nil, fmt.Errorf("%s: budget.max_stale must be at least budget.poll", path)
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
		// The node's default tools apply at launch: a backend that cannot turn
		// them off is refused here, not when the agent first starts.
		if err := builtin.Table().Check(p.WithToolDefaults(o.ProfileDefaults.Tools)); err != nil {
			return nil, fmt.Errorf("%s: with profile_defaults: %w", f, err)
		}
		if err := o.CheckAccount(p.Account); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if prev, dup := seen[p.Name]; dup {
			return nil, fmt.Errorf("%s: agent name %q already defined in %s", f, p.Name, prev)
		}
		seen[p.Name] = f
		cfg.Profiles = append(cfg.Profiles, p)
	}
	if err := checkGovernor(path, cfg.Orchestrator.Governor, seen); err != nil {
		return nil, err
	}
	if err := checkBindings(path, &cfg.Orchestrator, seen); err != nil {
		return nil, err
	}
	return cfg, nil
}

// governorPrefix is the shape of a governor prefix: a slash and one word.
var governorPrefix = regexp.MustCompile(`^/[a-z][a-z0-9_]{0,31}$`)

// checkGovernor validates the governor against the node's agents and the commands
// a governor may propose.
func checkGovernor(path string, g Governor, agents map[string]string) error {
	if g.Agent == "" {
		if len(g.Propose) > 0 || g.Prefix != "" {
			return fmt.Errorf("%s: governor.propose or governor.prefix is set but governor.agent is not", path)
		}
		return nil
	}
	if g.Prefix != "" && (!governorPrefix.MatchString(g.Prefix) || g.Prefix == command.Prefix) {
		return fmt.Errorf("%s: governor.prefix %q must be a \"/\" and one lowercase word, and not %s", path, g.Prefix, command.Prefix)
	}
	if agents[g.Agent] == "" {
		return fmt.Errorf("%s: governor.agent %q is not a configured agent", path, g.Agent)
	}
	for i, w := range g.Propose {
		if !command.ProposableWord(w) {
			return fmt.Errorf("%s: governor.propose[%d]: %q is not a command a governor may propose", path, i, w)
		}
	}
	return nil
}

// checkBindings validates the standing bindings against the node's agents,
// channels and admitted groups, and fills in their defaults.
func checkBindings(path string, o *Orchestrator, agents map[string]string) error {
	k := BindingKnown{
		Agent:    func(name string) bool { return agents[name] != "" },
		Groups:   map[string]bool{},
		Channels: map[string]bool{},
		Level:    o.Level,
		Governor: o.Governor.Agent,
	}
	for _, c := range o.Channels {
		k.Channels[c.Name] = true
		for _, g := range c.Groups {
			for _, n := range g.Names() {
				k.Groups[n] = true
			}
		}
	}
	seen := map[string]int{}
	for i := range o.Bindings {
		at := fmt.Sprintf("%s: bindings[%d]", path, i)
		if err := CheckBinding(&o.Bindings[i], k); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		b := o.Bindings[i]
		key := b.Key()
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%s: agent %q is already bound to %s at bindings[%d]", at, b.Agent, b.To, prev)
		}
		seen[key] = i
	}
	return nil
}

// BindingKnown is what a binding is checked against: the node's agents, the
// names of its channels and admitted groups, and its level.
type BindingKnown struct {
	Agent    func(name string) bool
	Groups   map[string]bool
	Channels map[string]bool
	Level    int
	// Governor is the governor's name, empty for none: its output goes only to
	// whoever asked it, so it takes no binding.
	Governor string
}

// CheckBinding validates one binding and fills in its defaults: the stream
// stdout and a stdout binding's level the node's. Whether a subject exists is not known to the config, so a
// private destination's subject is not checked here.
func CheckBinding(b *Binding, k BindingKnown) error {
	if b.Stream == "" {
		b.Stream = StreamStdout
	}
	stderrSet := b.Stream == StreamStderr && (b.Level != 0 || b.Prefix != "")
	if b.Level == 0 && b.Stream == StreamStdout {
		b.Level = BindingLevel(k.Level)
	}
	d, err := ParseDestination(b.To)
	switch {
	case !k.Agent(b.Agent):
		return fmt.Errorf("agent %q is not a configured agent", b.Agent)
	case k.Governor != "" && b.Agent == k.Governor:
		return fmt.Errorf("agent %q is the governor: what it says goes only where it was asked", b.Agent)
	case b.Stream != StreamStdout && b.Stream != StreamStderr:
		return fmt.Errorf("stream must be %q or %q", StreamStdout, StreamStderr)
	case stderrSet:
		return fmt.Errorf("a %s binding takes no level and no prefix", StreamStderr)
	case err != nil:
		return err
	case d.Kind == DestGroup && !k.Groups[d.Name]:
		return fmt.Errorf("no admitted group is named %q", d.Name)
	case d.Kind == DestPrivate && d.Channel != "" && !k.Channels[d.Channel]:
		return fmt.Errorf("no channel is named %q", d.Channel)
	case d.Kind == DestChannel && !reservedChannelNames[d.Name]:
		return fmt.Errorf("channel %q has several conversations: name one as %s:<name> or %s:<subject>", d.Name, DestGroup, DestPrivate)
	case b.Stream == StreamStdout && b.Level < 1:
		return fmt.Errorf("level must be at least 1")
	case utf8.RuneCountInString(b.Prefix) > MaxPrefix:
		return fmt.Errorf("prefix is longer than %d characters", MaxPrefix)
	case strings.ContainsAny(b.Prefix, "\r\n"):
		return fmt.Errorf("prefix is one line")
	}
	return nil
}

// Key identifies a checked binding: no agent is bound twice to one destination on
// one stream.
func (b Binding) Key() string { return b.Agent + "\x00" + b.Stream + "\x00" + b.To }

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
	if err := checkTools(p.Tools); err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	for _, t := range p.Tags {
		if !namePattern.MatchString(t) {
			return fmt.Errorf("tag %q must be lowercase letters, digits, '-' or '_'", t)
		}
	}
	if err := p.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	for _, h := range p.Egress.Pass {
		if err := egress.CheckHost(h); err != nil {
			return fmt.Errorf("egress.pass: %w", err)
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

// ProfileDefaults are the settings every agent profile takes unless it gives
// its own.
type ProfileDefaults struct {
	// Tools names the backend's own tools no agent is given unless its profile
	// gives a tools.disable list of its own, which then replaces this one.
	Tools core.Tools `yaml:"tools"`
}

// checkTools refuses a tool name that is empty, has space at either end or
// holds a control character: none of those names a tool.
func checkTools(t core.Tools) error {
	if t.Disable == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, name := range *t.Disable {
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("disable: %q is not a tool name", name)
		}
		if seen[name] {
			return fmt.Errorf("disable: %q is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}
