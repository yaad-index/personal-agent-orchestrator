package defaultagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func path(t *testing.T) string { return filepath.Join(t.TempDir(), "defaults.json") }

func TestAMissingFileIsAnEmptyStore(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, ok := s.Get("chat")
	assert.False(t, ok)
	assert.Equal(t, Document{Defaults: map[string]string{}}, s.Document())
}

func TestADefaultSurvivesAReopenWithItsVersion(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	c, err := s.Set("chat", "a")
	require.NoError(t, err)
	assert.Equal(t, Change{}, c)
	c, err = s.Set("chat", "b")
	require.NoError(t, err)
	assert.Equal(t, Change{Previous: "a"}, c)

	again, err := Open(p)
	require.NoError(t, err)
	got, ok := again.Get("chat")
	assert.True(t, ok)
	assert.Equal(t, "b", got)
	assert.Equal(t, uint64(2), again.Document().Version)

	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAnAgentIsTheDefaultOfAtMostOneChannel(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	c, err := s.Set("other", "a")
	require.NoError(t, err)
	assert.Equal(t, Change{Left: "chat"}, c, "setting it elsewhere moves it")
	_, ok := s.Get("chat")
	assert.False(t, ok)
	got, _ := s.Get("other")
	assert.Equal(t, "a", got)
}

func TestSettingTheSameDefaultAgainChangesNothing(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	c, err := s.Set("chat", "a")
	require.NoError(t, err)
	assert.Equal(t, Change{Previous: "a"}, c)
	assert.Equal(t, uint64(1), s.Document().Version)
}

func TestClearRemovesTheDefaultAndIsPersisted(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	c, err := s.Clear("chat")
	require.NoError(t, err)
	assert.Equal(t, Change{Previous: "a"}, c)
	c, err = s.Clear("chat")
	require.NoError(t, err)
	assert.Equal(t, Change{}, c, "clearing what is not set is not an error")

	again, err := Open(p)
	require.NoError(t, err)
	_, ok := again.Get("chat")
	assert.False(t, ok)
	assert.Equal(t, uint64(2), again.Document().Version)
}

func TestAChangeThatCannotBeWrittenIsNotMade(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "defaults.json"))
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		_ = f.Close()
		t.Skip("the directory is still writable (running as root?)")
	}

	_, err = s.Set("chat", "b")
	require.Error(t, err)
	got, _ := s.Get("chat")
	assert.Equal(t, "a", got)
	assert.Equal(t, uint64(1), s.Document().Version)
}

func TestAChangeWhoseFileCannotBeReplacedIsNotMade(t *testing.T) {
	p := path(t)
	s, err := Open(p)
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	// A directory where the file goes: the new file is written, then cannot take
	// its place.
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.MkdirAll(filepath.Join(p, "in-the-way"), 0o700))

	_, err = s.Set("chat", "b")
	require.Error(t, err)
	got, _ := s.Get("chat")
	assert.Equal(t, "a", got)
	assert.Equal(t, uint64(1), s.Document().Version)
}

func TestABadFileIsRefusedNotGuessed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "{",
		"empty agent":   `{"version":1,"defaults":{"chat":""}}`,
		"agent on two":  `{"version":1,"defaults":{"chat":"a","other":"a"}}`,
		"empty channel": `{"version":1,"defaults":{"":"a"}}`,
	} {
		p := path(t)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		_, err := Open(p)
		assert.Error(t, err, name)
	}
}

func TestTheDocumentIsACopy(t *testing.T) {
	s, err := Open(path(t))
	require.NoError(t, err)
	_, err = s.Set("chat", "a")
	require.NoError(t, err)
	d := s.Document()
	d.Defaults["chat"] = "x"
	got, _ := s.Get("chat")
	assert.Equal(t, "a", got)
}
