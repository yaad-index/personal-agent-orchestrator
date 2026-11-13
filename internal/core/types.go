package core

import (
	"context"
	"errors"
)

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

// Message is the source-agnostic input handed to routing. The message model
// (ADR-0010) extends it with an id, kind, attachments and the rest.
type Message struct {
	Sender string
	Text   string
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
