// Package agenttoken issues the credential an agent uses to call pao as itself
// (ADR-0016): a random token minted when the agent's session starts, held only
// in memory, and revoked when the session ends. It identifies a live session,
// not a name: a name is something anything that can read another agent's
// environment can claim.
//
// It is not a boundary against a process of the same OS user that can read the
// agent's environment: that process can use the token while the session lives.
// What it adds over a bare name is that the identity lasts only as long as the
// session, and can be revoked.
package agenttoken

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Registry maps live tokens to agent names. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	byToken map[string]string
	byAgent map[string]string
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{byToken: make(map[string]string), byAgent: make(map[string]string)}
}

// Mint issues a new token for the agent and revokes any it held, so an agent
// has one live token: its current session's.
func (r *Registry) Mint(agent string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// No randomness, no credential: a token that cannot be made is not made.
		panic("agenttoken: no randomness: " + err.Error())
	}
	token := hex.EncodeToString(b[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byAgent[agent]; ok {
		delete(r.byToken, old)
	}
	r.byAgent[agent] = token
	r.byToken[token] = agent
	return token
}

// Revoke ends the agent's token. Revoking an agent with none is a no-op.
func (r *Registry) Revoke(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byAgent[agent]; ok {
		delete(r.byToken, old)
		delete(r.byAgent, agent)
	}
}

// Resolve returns the agent a live token belongs to.
func (r *Registry) Resolve(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.byToken[token]
	return name, ok
}
