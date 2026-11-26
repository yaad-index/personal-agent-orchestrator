package core

import "context"

// SecretResolver resolves a secret reference to its value at run time, so the
// value never lives in a task file or on a channel (ADR-0037 point 3). It is
// the Secrets plane of ADR-0039. An error never carries the value.
type SecretResolver interface {
	Resolve(name string) ([]byte, error)
}

// SecretResolverFunc adapts a function to SecretResolver.
type SecretResolverFunc func(name string) ([]byte, error)

// Resolve calls f.
func (f SecretResolverFunc) Resolve(name string) ([]byte, error) { return f(name) }

// EgressPolicy is everything an agent's outbound traffic may reach. A host on
// neither list is refused.
type EgressPolicy struct {
	// Pass lists hosts reached through an untouched tunnel: the connection is
	// not intercepted and no credential is put into it.
	Pass []string
	// Inject lists the credentials put into the agent's calls on its behalf.
	Inject []InjectRule
}

// InjectRule puts one secret into the agent's calls to the hosts it names.
// The agent is given only a placeholder, in the variable Env; the placeholder
// is replaced by the secret's value in a request header on its way to one of
// Hosts, and an echo of the value in a response is put back to the
// placeholder.
type InjectRule struct {
	Secret string
	Hosts  []string
	Env    string
}

// Egress is what the agent's launch needs from an opened policy.
type Egress struct {
	// Env is added to the agent's environment: the proxy address, the trust
	// files, and each placeholder.
	Env []string
	// Socket is the Unix socket the agent's only route out connects to.
	Socket string
}

// Connectivity mediates an agent's outbound calls (the Connectivity plane of
// ADR-0039): it opens a policy for the agent before it starts and closes it
// when the agent's session ends.
type Connectivity interface {
	Open(ctx context.Context, agent string, p EgressPolicy) (Egress, error)
	Close(agent string) error
}
