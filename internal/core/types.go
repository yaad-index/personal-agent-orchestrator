package core

import (
	"context"
	"errors"
	"time"
)

// ReservedNames are sender names pao itself speaks as. An agent must not carry
// one: what an agent reads is the `[sender]: text` line, so an agent named like
// pao or the operator could pass its words off as theirs.
var ReservedNames = []string{"pao", "operator"}

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
	// Levels overrides DefaultLevels per kind (ADR-0008 point 2). Kinds it
	// leaves out keep their default level.
	Levels Levels `yaml:"levels"`
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
	// Reply is the ID of the message this one answers, if any.
	Reply       string
	Attachments []Attachment
	Reactions   []Reaction
}

// Line renders the message as the one line the agent reads: `[sender]: text`
// (ADR-0001 point 4).
func (m Message) Line() string { return "[" + m.Sender + "]: " + m.Text }

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
