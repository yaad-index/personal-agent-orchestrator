package grants

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

func path(t *testing.T) string { return filepath.Join(t.TempDir(), "grants.json") }

var (
	cell  = access.Grant{Subject: access.Agent("a"), Kind: access.ResourceAgent, Name: "b"}
	scope = access.Grant{Subject: access.Agent("a"), Kind: access.ResourceAgent, Tag: "team"}
	other = access.Grant{Subject: access.Agent("z"), Kind: access.ResourceAgent, Name: "b"}
)

func TestAMissingFileIsAnEmptyStore(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	assert.Empty(t, s.Of(access.Agent("a")))
	assert.Equal(t, Document{Grants: []Entry{}}, s.Document())
}

func TestGrantsSurviveAReopenWithTheirVersion(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	for _, g := range []access.Grant{other, scope, cell} {
		added, err := s.Add(g)
		require.NoError(t, err)
		assert.True(t, added)
	}

	again, err := Open(p)
	require.NoError(t, err)
	assert.ElementsMatch(t, []access.Grant{cell, scope}, again.Of(access.Agent("a")))
	assert.Equal(t, []access.Grant{other}, again.Of(access.Agent("z")))
	assert.Equal(t, Document{Version: 3, Grants: []Entry{
		{Subject: "agent:a", Kind: "agent", Tag: "team"},
		{Subject: "agent:a", Kind: "agent", Name: "b"},
		{Subject: "agent:z", Kind: "agent", Name: "b"},
	}}, again.Document())

	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAddingAGrantHeldAndRemovingOneNotHeldChangeNothing(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, err = s.Add(cell)
	require.NoError(t, err)
	added, err := s.Add(cell)
	require.NoError(t, err)
	assert.False(t, added)
	removed, err := s.Remove(scope)
	require.NoError(t, err)
	assert.False(t, removed)
	assert.Equal(t, uint64(1), s.Document().Version)

	removed, err = s.Remove(cell)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Empty(t, s.Of(access.Agent("a")))
	assert.Equal(t, uint64(2), s.Document().Version)
}

func TestABadGrantIsNotAdded(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, err = s.Add(access.Grant{Subject: access.Agent("a"), Kind: access.ResourceAgent, Name: "b", Tag: "t"})
	require.Error(t, err)
	assert.Equal(t, uint64(0), s.Document().Version)
}

func TestAChangeWhoseFileCannotBeReplacedIsNotMade(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	_, err = s.Add(cell)
	require.NoError(t, err)
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.MkdirAll(filepath.Join(p, "in-the-way"), 0o700))

	_, err = s.Add(scope)
	require.Error(t, err)
	_, err = s.Remove(cell)
	require.Error(t, err)
	assert.Equal(t, []access.Grant{cell}, s.Of(access.Agent("a")))
	assert.Equal(t, uint64(1), s.Document().Version)
}

func TestABadFileIsRefusedNotGuessed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     "{",
		"bad subject":  `{"version":1,"grants":[{"subject":"bee","kind":"agent","name":"b"}]}`,
		"name and tag": `{"version":1,"grants":[{"subject":"agent:a","kind":"agent","name":"b","tag":"t"}]}`,
		"neither":      `{"version":1,"grants":[{"subject":"agent:a","kind":"agent"}]}`,
		"unknown kind": `{"version":1,"grants":[{"subject":"agent:a","kind":"channel","name":"b"}]}`,
		"repeated":     `{"version":1,"grants":[{"subject":"agent:a","kind":"agent","name":"b"},{"subject":"agent:a","kind":"agent","name":"b"}]}`,
	} {
		p := path(t)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		_, err := Open(p)
		assert.Error(t, err, name)
	}
}

func TestTheStoreDrivesTheMatrix(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	m := access.Matrix{Grants: s}
	b := access.AgentResource("b", nil)
	assert.False(t, m.Allow(access.Agent("a"), access.Route, b))
	_, err = s.Add(cell)
	require.NoError(t, err)
	assert.True(t, m.Allow(access.Agent("a"), access.Route, b), "a grant takes effect at once")
	_, err = s.Remove(cell)
	require.NoError(t, err)
	assert.False(t, m.Allow(access.Agent("a"), access.Route, b), "and a revocation too")
}

func TestASeedIsWrittenOnceIntoAnEmptyStoreOnly(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	assert.False(t, s.Found())
	op := access.Grant{Subject: access.Operator, Kind: access.ResourceAgent, Tag: "home"}
	require.NoError(t, s.Seed([]access.Grant{op, op, cell}))
	assert.Equal(t, uint64(1), s.Document().Version, "one write")
	assert.Equal(t, []access.Grant{op}, s.Of(access.Operator), "repeats are folded")
	assert.Error(t, s.Seed([]access.Grant{scope}), "not twice")

	again, err := Open(p)
	require.NoError(t, err)
	assert.True(t, again.Found())
	assert.Error(t, again.Seed([]access.Grant{scope}), "never over a stored document")

	fresh, err := Open(path(t))
	require.NoError(t, err)
	_, err = fresh.Add(cell)
	require.NoError(t, err)
	assert.Error(t, fresh.Seed([]access.Grant{scope}), "never over grants made since")
	assert.Error(t, (&Store{path: path(t)}).Seed([]access.Grant{{Subject: access.Operator, Kind: access.ResourceAgent}}), "a bad grant is refused")
}
