package runsecret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const value = "sk-THE-SECRET-VALUE"

func store(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: filepath.Join(t.TempDir(), "run-secrets"), Resolve: func(name string) ([]byte, error) {
		switch name {
		case "report-api":
			return []byte(value), nil
		case "empty":
			return nil, nil
		}
		return nil, errors.New("no secret " + name)
	}}
}

func TestASecretIsAFileOnlyTheNodeUserReadsAndTheAgentGetsItsPath(t *testing.T) {
	s := store(t)
	path, err := s.Materialize("tmp-a", "report-api")
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, value, string(b))
	for p, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700, s.Dir: 0o700} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, mode, info.Mode().Perm(), p)
	}
	assert.Equal(t, []string{"PAO_SECRET_REPORT_API=" + path}, s.Env("tmp-a"), "the path, never the value")
	assert.Empty(t, s.Env("tmp-b"))
	assert.Equal(t, "PAO_SECRET_REPORT_API", EnvName("report-api"))
}

func TestARunsSecretsAreMadeOnceAndNeverAddedToAnothers(t *testing.T) {
	s := store(t)
	_, err := s.Materialize("tmp-a", "report-api")
	require.NoError(t, err)
	_, err = s.Materialize("tmp-a", "report-api")
	assert.Error(t, err, "an agent that already has a run directory gets no second")
	assert.Len(t, s.Env("tmp-a"), 1)
}

func TestAFailedResolveLeavesNothing(t *testing.T) {
	s := store(t)
	for _, name := range []string{"unknown", "empty"} {
		_, err := s.Materialize("tmp-a", name)
		require.Error(t, err, name)
		assert.NotContains(t, err.Error(), value)
		assert.NoDirExists(t, filepath.Join(s.Dir, "tmp-a"), name)
	}
}

func TestNamesCannotNameAPathOutsideTheStore(t *testing.T) {
	s := store(t)
	for _, c := range [][2]string{{"../x", "report-api"}, {"tmp-a", "../x"}, {"", "report-api"}, {"tmp-a", ""}} {
		_, err := s.Materialize(c[0], c[1])
		assert.Error(t, err, "%v", c)
	}
	assert.Error(t, s.Wipe("../x"))
	assert.Nil(t, s.Env("../x"))
}

func TestWipeAndSweepRemoveTheSecrets(t *testing.T) {
	s := store(t)
	a, err := s.Materialize("tmp-a", "report-api")
	require.NoError(t, err)
	b, err := s.Materialize("tmp-b", "report-api")
	require.NoError(t, err)
	require.NoError(t, s.Wipe("tmp-a"))
	assert.NoFileExists(t, a)
	assert.FileExists(t, b, "only that agent's")
	require.NoError(t, s.Wipe("tmp-a"), "wiping again is not an error")
	require.NoError(t, s.Sweep())
	assert.NoFileExists(t, b)
	assert.DirExists(t, s.Dir)
	require.NoError(t, (&Store{Dir: filepath.Join(t.TempDir(), "never-made")}).Sweep(), "nothing to sweep")
}

func TestASecretNameCannotNameAPathEvenIfItResolves(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: filepath.Join(root, "run-secrets"), Resolve: func(string) ([]byte, error) { return []byte(value), nil }}
	for _, name := range []string{"../../escaped", "a/b", "UPPER"} {
		_, err := s.Materialize("tmp-a", name)
		assert.Error(t, err, name)
	}
	assert.NoFileExists(t, filepath.Join(root, "escaped"))
	assert.NoDirExists(t, filepath.Join(s.Dir, "tmp-a"))
}
