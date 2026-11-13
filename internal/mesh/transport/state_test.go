package transport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMastersAreOrderedByEpochThenLowerNodeID(t *testing.T) {
	cases := []struct {
		ea     uint64
		ma     string
		eb     uint64
		mb     string
		wantAB bool
	}{
		{2, "n2", 1, "n1", true},  // higher epoch wins whatever the ids
		{1, "n1", 2, "n2", false}, // and loses to it
		{3, "b", 3, "a", false},   // equal epochs: the lower id stays master
		{3, "a", 3, "b", true},    // so the higher id yields
		{3, "a", 3, "a", false},   // a master does not supersede itself
		{0, "n1", 0, "n1", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.wantAB, Supersedes(c.ea, c.ma, c.eb, c.mb), "%d/%s over %d/%s", c.ea, c.ma, c.eb, c.mb)
	}
	// A total order: for two distinct masters exactly one supersedes the other.
	for _, pair := range [][4]any{{uint64(1), "a", uint64(1), "b"}, {uint64(1), "a", uint64(2), "a"}, {uint64(5), "z", uint64(5), "a"}} {
		ab := Supersedes(pair[0].(uint64), pair[1].(string), pair[2].(uint64), pair[3].(string))
		ba := Supersedes(pair[2].(uint64), pair[3].(string), pair[0].(uint64), pair[1].(string))
		assert.NotEqual(t, ab, ba, "%v: a heal always has a winner", pair)
	}
}

func TestAFreshNodeBeginsAtTheDocumentsMasterAtEpochZero(t *testing.T) {
	m, err := LoadMembership(filepath.Join(t.TempDir(), "state"), "n2", "n1")
	require.NoError(t, err)
	e, master := m.View()
	assert.Equal(t, uint64(0), e)
	assert.Equal(t, "n1", master)
	assert.False(t, m.IsMaster())

	m, err = LoadMembership("", "n1", "n1")
	require.NoError(t, err)
	assert.True(t, m.IsMaster())
}

func TestABeliefIsAdoptedOnlyIfItSupersedesAndIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mesh.state")
	m, err := LoadMembership(path, "n1", "n1")
	require.NoError(t, err)
	require.True(t, m.IsMaster())

	changed, err := m.Observe(0, "n2")
	require.NoError(t, err)
	assert.False(t, changed, "equal epoch, higher id: n1 stays master")
	assert.True(t, m.IsMaster())

	changed, err = m.Observe(1, "n2")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, m.IsMaster(), "a master that hears of a superseding one has yielded")

	changed, err = m.Observe(0, "n1")
	require.NoError(t, err)
	assert.False(t, changed, "an older belief does not roll it back")

	again, err := LoadMembership(path, "n1", "n1")
	require.NoError(t, err)
	e, master := again.View()
	assert.Equal(t, uint64(1), e, "a restart does not forget the epoch the mesh moved to")
	assert.Equal(t, "n2", master)
	assert.False(t, again.IsMaster())
}

func TestObservingNothingChangesNothing(t *testing.T) {
	m, err := LoadMembership("", "n1", "n1")
	require.NoError(t, err)
	changed, err := m.Observe(9, "")
	require.NoError(t, err)
	assert.False(t, changed, "a belief with no master is not a belief")
}

func TestACorruptStateFileIsAnErrorNotAFreshStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mesh.state")
	for _, body := range []string{"not json", `{"epoch":3}`, `{"epoch":3,"master":""}`} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		_, err := LoadMembership(path, "n1", "n1")
		assert.Error(t, err, body)
	}
}

func TestPersistingLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadMembership(filepath.Join(dir, "mesh.state"), "n1", "n1")
	require.NoError(t, err)
	_, err = m.Observe(4, "n2")
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "mesh.state", entries[0].Name())
}
