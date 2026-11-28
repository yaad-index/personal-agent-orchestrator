package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ReservedNames are sender names pao itself speaks as. An agent must not carry
// one: what an agent reads is the `[sender]: text` line, so an agent named like
// pao or the operator could pass its words off as theirs.
var ReservedNames = []string{"pao", "operator"}

// CommandWords are the words of pao's chat commands (ADR-0026 point 9). An agent
// must not carry one as its name: "/pao <name> <message>" addresses an agent, so an
// agent named like a command could never be reached that way. The command table
// is checked against this list.
var CommandWords = []string{"agent", "agents", "allow", "always", "bind", "bindings", "deny", "file", "help", "kill", "log", "spawn", "stream", "tag", "unbind", "view"}

// Profile is the agent-intrinsic configuration of one agent (ADR-0001 point 4).
// Only the fields the built milestones consume are present; later milestones
// add the per-backend specifics ADR-0011 lists (transcript, hooks, startup).
type Profile struct {
	// Name addresses exactly one agent. Names are unique within a node.
	Name string `yaml:"name"`
	// Tags address a capability or role; every agent carrying the tag matches.
	Tags []string `yaml:"tags"`
	// Backend names the coding-agent harness that runs this agent.
	Backend string `yaml:"backend"`
	// Workdir is the directory the agent runs in.
	Workdir string `yaml:"workdir"`
	// Startup is the backend's startup procedure (ADR-0007 point 2).
	Startup Startup `yaml:"startup"`
	// Transcript says where the backend writes its transcript and how to read
	// it (ADR-0008, ADR-0011).
	Transcript Transcript `yaml:"transcript"`
	// Node names the node that hosts the agent when it is not this one. It is set
	// only on the profile of an agent the master knows from the mesh directory, never
	// read from a profile file.
	Node string `yaml:"-"`
	// Provisioned marks a temporary agent's profile, whose working directory pao
	// provisioned from a source rather than the operator (ADR-0006 point 2). Its
	// backend loads none of the settings in that directory (ADR-0033 point 1). It
	// is set by pao, never read from a file.
	Provisioned bool `yaml:"-"`
	// Levels overrides DefaultLevels per kind (ADR-0008 point 2). Kinds it
	// leaves out keep their default level.
	Levels Levels `yaml:"levels"`
	// Egress is where the agent's calls may go when it runs behind the egress
	// plane (ADR-0049).
	Egress EgressProfile `yaml:"egress"`
	// Inherit names the categories of the node user's own environment the agent
	// loads (ADR-0025 point 3). With none, it loads only its profile's and pao's.
	Inherit []string `yaml:"inherit"`
	// Tools is what the profile turns off of its backend's own tools.
	Tools Tools `yaml:"tools"`
	// Account names the account the agent draws on (ADR-0015 point 3), one of
	// the node's configured accounts. The account's credential stays in the
	// orchestrator config, never here (ADR-0015 point 4).
	Account string `yaml:"account"`
	// User is the subject the agent belongs to. Only a channel the same subject
	// owns may be the agent's (ADR-0058 point 9); an agent with no user has no
	// channel.
	User string `yaml:"user"`
	// LogChannel is the agent's log channel, by name: one of its user's group
	// or topic channels, shown all of the agent's output unfiltered and taking
	// no input (ADR-0058 point 7). Empty, the default, is none.
	LogChannel string `yaml:"log_channel"`
	// MessagePrefix is how a message the agent reads carries its sender, in
	// place of the node's setting: off, name or channel (ADR-0058 point 10).
	MessagePrefix string `yaml:"message_prefix"`
	// Budget is how the agent's headroom is judged from its account's figure.
	Budget BudgetRule `yaml:"budget"`
}

// The windows of an account's figure a budget rule may weigh.
const (
	WindowWeek     = "week"
	WindowFiveHour = "five_hour"
)

// BudgetRule is how an agent's headroom is judged from its account's figure
// (ADR-0035): the windows weighed, and the share each must keep above. Its zero
// value weighs every window with a floor of 0, so any share left in each is
// headroom. It has no bearing on an unmetered account.
type BudgetRule struct {
	// Windows names the windows weighed; empty weighs them all.
	Windows []string `yaml:"windows"`
	// Floor is the share, in percent, each weighed window must keep above.
	Floor float64 `yaml:"floor"`
}

// Validate reports whether the rule names known windows, each once, and a floor
// from 0 up to but not including 100.
func (r BudgetRule) Validate() error {
	seen := map[string]bool{}
	for _, w := range r.Windows {
		if w != WindowWeek && w != WindowFiveHour {
			return fmt.Errorf("window %q is not one of %s, %s", w, WindowWeek, WindowFiveHour)
		}
		if seen[w] {
			return fmt.Errorf("window %q is named twice", w)
		}
		seen[w] = true
	}
	if r.Floor < 0 || r.Floor >= 100 {
		return fmt.Errorf("floor %v must be at least 0 and below 100", r.Floor)
	}
	return nil
}

// Weighs reports whether the rule weighs window w.
func (r BudgetRule) Weighs(w string) bool {
	return len(r.Windows) == 0 || slices.Contains(r.Windows, w)
}

// Transcript locates and names the format of a backend's transcript.
type Transcript struct {
	// Format names the reader for the backend's transcript format.
	Format string `yaml:"format"`
	// Path is the transcript file, or a directory whose most recently written
	// .jsonl file is the live transcript.
	Path string `yaml:"path"`
}

// EffectiveLevels returns the profile's levels: the defaults with the profile's
// own entries laid over them.
func (p Profile) EffectiveLevels() Levels {
	out := DefaultLevels()
	for k, v := range p.Levels {
		out[k] = v
	}
	return out
}

// Startup declares how a backend is brought to a ready prompt.
type Startup struct {
	// Command is the shell command that starts the backend in the terminal.
	Command string `yaml:"command"`
	// ReadyPattern is a regular expression that matches the pane once the
	// backend is ready for input.
	ReadyPattern string `yaml:"ready_pattern"`
	// ReadyTimeout bounds how long startup may take before the operator is
	// shown the pane instead.
	ReadyTimeout time.Duration `yaml:"ready_timeout"`
}

// HasTag reports whether the profile carries tag.
func (p Profile) HasTag(tag string) bool {
	for _, t := range p.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// State is the lifecycle state the registry tracks for an agent.
type State int

const (
	// Stopped means no running agent exists for the profile.
	Stopped State = iota
	// Running means a ready agent exists and can receive input.
	Running
)

func (s State) String() string {
	if s == Running {
		return "running"
	}
	return "stopped"
}

// TargetKind says how an input addresses its destination (ADR-0003 point 2).
type TargetKind int

const (
	// ByName addresses one specific agent.
	ByName TargetKind = iota + 1
	// ByTag addresses every agent carrying the tag.
	ByTag
)

// Target is the explicit address of an input. Selecting a destination for an
// input that names none is deferred by ADR-0003 and is not modelled here.
type Target struct {
	Kind  TargetKind
	Value string
}

// MessageKind says what a message is (ADR-0010 point 1).
type MessageKind string

const (
	// MessageContent is ordinary content from a human or an agent.
	MessageContent MessageKind = "content"
	// MessageSystemEvent is a message pao raised itself (an approval prompt, a
	// control-plane alert, an expanded reminder).
	MessageSystemEvent MessageKind = "system_event"
)

// Format says how a message's text is written (ADR-0050 point 1).
type Format string

const (
	// FormatPlain is text shown as it is. A message that names no format is
	// plain.
	FormatPlain Format = ""
	// FormatMarkdown is an agent's own words: Markdown, which a channel shows in
	// its own native formatting.
	FormatMarkdown Format = "markdown"
)

// Attachment is a normalized attachment. The normalization format is left open
// by ADR-0010; this is the minimal common shape.
type Attachment struct {
	Name string
	MIME string
	// Ref locates the content (a path or a store key), never the content itself.
	Ref string
}

// Reaction is who reacted to a message with which emoji (ADR-0010 point 4).
type Reaction struct {
	By    string
	Emoji string
}

// Action is one choice offered with a message (ADR-0027 point 1): a short label
// and the typed command it stands for. A channel that shows buttons draws it as
// one; a press is that command from whoever pressed.
type Action struct {
	Label   string
	Command string
}

// Message is the source-agnostic message pao carries across all sources
// (ADR-0010 point 1). The agent reads only Line(); everything else is
// orchestrator-side.
type Message struct {
	// ID is pao's identifier for the message, assigned when it is stored.
	ID     string
	Time   time.Time
	Sender string
	Kind   MessageKind
	Text   string
	// Channel is the channel the message came in on, where it came in on one.
	Channel string
	// Format is how Text is written. Only an agent's own words are Markdown;
	// what pao or a human writes is plain (ADR-0050 point 2).
	Format Format
	// Reply is the ID of the message this one answers, if any.
	Reply string
	// ReplyQuote is set only on the copy of a message that goes to an agent on
	// another node: a snippet of the message being answered, which that node
	// resolves in the agent's own scope. It is never stored.
	ReplyQuote  string
	Attachments []Attachment
	Reactions   []Reaction
	// Actions are the choices offered with the message. Every action is also a
	// command the text names, so a channel that shows no buttons loses nothing.
	Actions []Action
	// Label, when set, is shown to the human before the text in place of the
	// sender's name: a standing binding's prefix (ADR-0029 point 5). It is never
	// stored and an agent never reads it.
	Label string
}

// Line renders the message as the one line the agent reads: `[sender]: text`
// (ADR-0001 point 4).
func (m Message) Line() string { return "[" + m.Sender + "]: " + m.Text }

// The ways a message can carry its sender to the agent (ADR-0058 point 10).
const (
	// PrefixOff shows the text alone. It is the default.
	PrefixOff = "off"
	// PrefixName shows the sender's name first, as `[name]: text`.
	PrefixName = "name"
	// PrefixChannel shows the sender and the channel the message came in on,
	// as `[name@channel]: text`; one that came in on no channel as PrefixName.
	PrefixChannel = "channel"
)

// Prefixes are the values a message prefix may take.
var Prefixes = []string{PrefixOff, PrefixName, PrefixChannel}

// LineWith renders the message as the agent reads it under a prefix setting;
// an empty one is PrefixOff.
func (m Message) LineWith(prefix string) string {
	switch {
	case prefix == PrefixName, prefix == PrefixChannel && m.Channel == "":
		return m.Line()
	case prefix == PrefixChannel:
		return "[" + m.Sender + "@" + m.Channel + "]: " + m.Text
	}
	return m.Text
}

// Shown renders the message as a human is shown it: after its label where it has
// one, otherwise as Line.
func (m Message) Shown() string {
	if m.Label != "" {
		return m.Label + " " + m.Text
	}
	return m.Line()
}

// Launcher brings an agent up from its profile and returns once the agent is
// ready to receive input, or with the reason it is not (ADR-0007 point 2).
type Launcher interface {
	Launch(ctx context.Context, p Profile) error
}

// Deliverer hands a message to a running agent (the terminal-in half of
// ADR-0001 point 3).
type Deliverer interface {
	Deliver(ctx context.Context, p Profile, m Message) error
}

var (
	// ErrNoTarget is returned for an input that names no target.
	ErrNoTarget = errors.New("input names no target")
	// ErrNoMatch is returned when a target matches no registered agent.
	ErrNoMatch = errors.New("target matches no agent")
)

// Tools names the backend's own tools an agent is not given (#179). The tools
// pao offers through its MCP surface are granted separately (ADR-0016).
type Tools struct {
	// Disable names the tools turned off, as the backend names them. Left out,
	// the node's profile defaults apply; given, even as an empty list, it
	// replaces them.
	Disable *[]string `yaml:"disable"`
}

// WithToolDefaults is p with the node's default tools applied: the profile's
// own list where it gives one, the default otherwise.
func (p Profile) WithToolDefaults(d Tools) Profile {
	if p.Tools.Disable == nil && d.Disable != nil {
		list := slices.Clone(*d.Disable)
		p.Tools.Disable = &list
	}
	return p
}

// DisabledTools is the list of tools p turns off, empty when it turns off none.
func (p Profile) DisabledTools() []string {
	if p.Tools.Disable == nil {
		return nil
	}
	return *p.Tools.Disable
}
