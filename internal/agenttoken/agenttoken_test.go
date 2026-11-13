package agenttoken

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAMintedTokenResolvesToItsAgent(t *testing.T) {
	r := New()
	tok := r.Mint("a")
	name, ok := r.Resolve(tok)
	require.True(t, ok)
	assert.Equal(t, "a", name)
	assert.Len(t, tok, 64, "256 random bits")
}

func TestTokensAreDistinctPerAgentAndPerMint(t *testing.T) {
	r := New()
	a, b := r.Mint("a"), r.Mint("b")
	assert.NotEqual(t, a, b)
	a2 := r.Mint("a")
	assert.NotEqual(t, a, a2)
}

func TestMintingAgainRevokesTheOldTokenSoOnlyTheCurrentSessionHoldsOne(t *testing.T) {
	r := New()
	old := r.Mint("a")
	fresh := r.Mint("a")
	_, ok := r.Resolve(old)
	assert.False(t, ok, "a relaunch ends the previous session's identity")
	name, ok := r.Resolve(fresh)
	require.True(t, ok)
	assert.Equal(t, "a", name)
}

func TestRevokeEndsTheToken(t *testing.T) {
	r := New()
	tok := r.Mint("a")
	other := r.Mint("b")
	r.Revoke("a")
	_, ok := r.Resolve(tok)
	assert.False(t, ok)
	_, ok = r.Resolve(other)
	assert.True(t, ok, "another agent's token is untouched")
	r.Revoke("a")
	r.Revoke("never-minted")
}

func TestUnknownAndEmptyTokensResolveToNobody(t *testing.T) {
	r := New()
	r.Mint("a")
	for _, tok := range []string{"", "nope", "a", "PAO_AGENT_TOKEN"} {
		_, ok := r.Resolve(tok)
		assert.False(t, ok, tok)
	}
}

func TestConcurrentUse(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); r.Resolve(r.Mint("a")) }()
		go func() { defer wg.Done(); r.Revoke("a") }()
		go func() { defer wg.Done(); r.Resolve("x") }()
	}
	wg.Wait()
}
