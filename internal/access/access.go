// Package access is the seam where the access matrix (ADR-0005, ADR-0012)
// plugs in. The matrix itself is a later milestone; until it exists the only
// implementation is OperatorOnly, so the paths that agents will one day use
// are enforced now and stay closed.
package access

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
	// Kill tears a temporary agent down (ADR-0006 point 3).
	Kill Action = "kill"
	// ReadHarvest reads a harvested slice (ADR-0006 point 4).
	ReadHarvest Action = "read_harvest"
)

// Resource is what the action is on: an agent, by name and tags (a tag is an
// access scope, ADR-0012 point 3).
type Resource struct {
	Agent string
	Tags  []string
}

// Authorizer decides whether a subject may do an action on a resource.
// Anything it does not positively allow is denied.
type Authorizer interface {
	Allow(s Subject, a Action, r Resource) bool
}

// OperatorOnly allows the operator everything and every agent nothing. It is
// the default until the access matrix is built.
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
