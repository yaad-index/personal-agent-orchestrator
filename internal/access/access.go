// Package access is the access matrix (ADR-0005, ADR-0012): who may reach which
// resource. Matrix decides from grants; OperatorOnly is the closed fallback used
// where no matrix is given.
package access

import (
	"fmt"
	"slices"
	"strings"
)

// Kind says what kind of subject is asking.
type Kind int

const (
	// KindOperator is the human operator. It starts at 1 so that the zero
	// Subject is nobody and is denied.
	KindOperator Kind = iota + 1
	// KindAgent is an agent acting on its own account.
	KindAgent
)

// Subject is who is asking.
type Subject struct {
	Kind Kind
	// Name is the agent's name for KindAgent.
	Name string
}

// Operator is the human operator.
var Operator = Subject{Kind: KindOperator}

// Agent is the named agent as a requester.
func Agent(name string) Subject { return Subject{Kind: KindAgent, Name: name} }

// Action is what the subject wants to do.
type Action string

const (
	// List shows that an agent exists (name, tags, state).
	List Action = "list"
	// ListRecipes shows which recipes a temporary agent can be spawned from.
	ListRecipes Action = "list_recipes"
	// Route delivers a message to an agent (ADR-0003, ADR-0012 point 4).
	Route Action = "route"
	// Spawn starts a temporary agent (ADR-0006 point 2).
	Spawn Action = "spawn"
	// Kill tears a temporary agent down (ADR-0006 point 3).
	Kill Action = "kill"
	// ReadHarvest reads a harvested slice (ADR-0006 point 4).
	ReadHarvest Action = "read_harvest"
	// Watch reads an agent's rendered output on demand (ADR-0018 point 6), or
	// changes how it is shown.
	Watch Action = "watch"
	// Lifecycle starts, stops or restarts a configured agent (ADR-0007).
	Lifecycle Action = "lifecycle"
	// CallTool calls a tool pao offers on behalf of an upstream server (ADR-0024
	// point 5).
	CallTool Action = "call_tool"
)

// ResourceKind is the kind of a resource: a column group of the matrix (ADR-0005
// point 1: agents, tools, channels). Agents and tools are enforced so far.
type ResourceKind string

// The kinds of resource access is enforced for.
const (
	// ResourceAgent is an agent, by name and tags.
	ResourceAgent ResourceKind = "agent"
	// ResourceTool is a tool pao offers on its own MCP surface on behalf of an
	// upstream server (ADR-0024), by its pao-side name and the tags the operator
	// gave it.
	ResourceTool ResourceKind = "tool"
)

// Resource is what the action is on, by kind, name and tags (a tag is an access
// scope, ADR-0012 point 3).
type Resource struct {
	Kind ResourceKind
	Name string
	Tags []string
}

// AgentResource is the agent of that name and tags as a resource.
func AgentResource(name string, tags []string) Resource {
	return Resource{Kind: ResourceAgent, Name: name, Tags: tags}
}

// ToolResource is the tool of that pao-side name and tags as a resource.
func ToolResource(name string, tags []string) Resource {
	return Resource{Kind: ResourceTool, Name: name, Tags: tags}
}

// Authorizer decides whether a subject may do an action on a resource.
// Anything it does not positively allow is denied.
type Authorizer interface {
	Allow(s Subject, a Action, r Resource) bool
}

// OperatorOnly allows the operator everything and every agent nothing. It is the
// closed fallback where no Authorizer is given.
type OperatorOnly struct{}

// Allow implements Authorizer.
func (OperatorOnly) Allow(s Subject, _ Action, _ Resource) bool { return s.Kind == KindOperator }

// Allowed asks a for its decision, treating a nil Authorizer as OperatorOnly:
// a caller that forgot to supply one is closed, not open.
func Allowed(a Authorizer, s Subject, act Action, r Resource) bool {
	if a == nil {
		a = OperatorOnly{}
	}
	return a.Allow(s, act, r)
}

// String is the subject as grants name it: "operator", or "agent:<name>".
func (s Subject) String() string {
	switch s.Kind {
	case KindOperator:
		return "operator"
	case KindAgent:
		return "agent:" + s.Name
	}
	return ""
}

// ParseSubject reads a subject as String writes it.
func ParseSubject(v string) (Subject, error) {
	if v == "operator" {
		return Operator, nil
	}
	if name, ok := strings.CutPrefix(v, "agent:"); ok && name != "" && !strings.ContainsAny(name, " \t\n:") {
		return Agent(name), nil
	}
	return Subject{}, fmt.Errorf("subject %q is not \"operator\" or \"agent:<name>\"", v)
}

// Grant gives a subject one cell of the matrix (a resource by name) or a tag
// scope (every resource of the kind carrying the tag), ADR-0012 points 3 and 5.
// Exactly one of Name and Tag is set. A grant carries no actions: the matrix
// decides what a subject may reach, and the approval gate decides whether each
// action runs (ADR-0005).
type Grant struct {
	Subject Subject
	Kind    ResourceKind
	Name    string
	Tag     string
}

// Validate reports whether the grant is well formed.
func (g Grant) Validate() error {
	if _, err := ParseSubject(g.Subject.String()); err != nil {
		return fmt.Errorf("a grant names a subject: %w", err)
	}
	switch {
	case g.Kind != ResourceAgent && g.Kind != ResourceTool:
		return fmt.Errorf("resource kind %q is not %q or %q", g.Kind, ResourceAgent, ResourceTool)
	case (g.Name == "") == (g.Tag == ""):
		return fmt.Errorf("a grant names one resource or one tag, not both or neither")
	case strings.ContainsAny(g.Name+g.Tag, " \t\n"):
		return fmt.Errorf("a resource name or tag has no spaces")
	}
	return nil
}

// covers reports whether the grant reaches r.
func (g Grant) covers(r Resource) bool {
	if g.Kind != r.Kind {
		return false
	}
	if g.Name != "" {
		return g.Name == r.Name
	}
	return slices.Contains(r.Tags, g.Tag)
}

// Grants is where Matrix reads the grants of a subject, afresh at each decision.
type Grants interface {
	Of(s Subject) []Grant
}

// Matrix allows a subject an action on a resource when one of the subject's
// grants covers the resource; anything else is denied (ADR-0005 point 1). This
// holds for the operator too: the operator administers the grants, and is not
// itself granted anything it was not given (ADR-0012 point 2).
type Matrix struct {
	Grants Grants
}

// Allow implements Authorizer.
func (m Matrix) Allow(s Subject, _ Action, r Resource) bool {
	if m.Grants == nil {
		return false
	}
	if _, err := ParseSubject(s.String()); err != nil {
		return false
	}
	for _, g := range m.Grants.Of(s) {
		if g.covers(r) {
			return true
		}
	}
	return false
}
