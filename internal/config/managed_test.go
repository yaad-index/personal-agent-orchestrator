package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	chanOK = "    kind: bot\n    credential_env: PAO_BOT\n"
	// a channel in the managed file has a credential of its own (ADR-0057 point 3)
	chanUI = "    kind: bot\n    credential_env: PAO_BOT_UI\n"
)

// managedSetup writes a node whose pao.yaml holds own, naming managed.yaml,
// which holds managed unless it is empty.
func managedSetup(t *testing.T, own, managed string) (pao, mf string) {
	t.Helper()
	dir := t.TempDir()
	pao = filepath.Join(dir, "pao.yaml")
	write(t, pao, "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nmanaged: managed.yaml\n"+own)
	write(t, filepath.Join(dir, "profiles", "a.yaml"), "name: a\nbackend: claude-code\n"+startup)
	mf = filepath.Join(dir, "managed.yaml")
	if managed != "" {
		write(t, mf, managed)
	}
	return pao, mf
}

func TestTheManagedFilesChannelsJoinTheOperatorsOwn(t *testing.T) {
	pao, mf := managedSetup(t, "channels:\n  - id: 1\n    name: mine\n"+chanOK, "channels:\n  - id: 2\n    name: ui-made\n"+chanUI)
	cfg, err := Load(pao)
	require.NoError(t, err)
	o := cfg.Orchestrator
	require.Len(t, o.Channels, 2)
	assert.Equal(t, "mine", o.Channels[0].Name)
	assert.Equal(t, "ui-made", o.Channels[1].Name)
	assert.False(t, o.ManagedChannel("mine"))
	assert.True(t, o.ManagedChannel("ui-made"))
	assert.Equal(t, mf, o.ManagedPath(), "resolved against pao.yaml's directory")
}

func TestAManagedFileNotWrittenYetHoldsNothing(t *testing.T) {
	pao, _ := managedSetup(t, "", "")
	cfg, err := Load(pao)
	require.NoError(t, err)
	assert.Empty(t, cfg.Orchestrator.Channels)
}

func TestWithoutAManagedFileNothingIsManaged(t *testing.T) {
	path, err := channelsConfig(t, "channels:\n  - id: 1\n    name: mine\n"+chanOK)
	require.NoError(t, err)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Orchestrator.ManagedPath())
	assert.False(t, cfg.Orchestrator.ManagedChannel("mine"))
	assert.ErrorIs(t, WriteManaged(path, Managed{}), ErrNotManaged, "pao writes nothing it was not given a file for")
}

func TestAClashBetweenTheTwoFilesIsALoadErrorNamingBoth(t *testing.T) {
	for name, c := range map[string]struct{ own, managed, want string }{
		"same name": {"channels:\n  - id: 1\n    name: chat\n" + chanOK, "channels:\n  - id: 2\n    name: chat\n" + chanOK, `name "chat" is already used`},
		"same id":   {"channels:\n  - id: 1\n    name: a\n" + chanOK, "channels:\n  - id: 1\n    name: b\n" + chanOK, "id 1 is already channel"},
		"same group name": {
			"channels:\n  - id: 1\n    name: a\n" + chanOK + "    groups:\n      - id: \"-1\"\n        name: crew\n        members: operators\n",
			"channels:\n  - id: 2\n    name: b\n" + chanOK + "    groups:\n      - id: \"-2\"\n        name: crew\n        members: operators\n",
			`name "crew" is already`,
		},
	} {
		pao, mf := managedSetup(t, c.own, c.managed)
		_, err := Load(pao)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
		assert.Contains(t, err.Error(), mf, name+": the managed file is named")
		assert.Contains(t, err.Error(), pao, name+": and so is pao.yaml")
	}
}

func TestTheManagedFileHoldsOnlyWhatPaoManages(t *testing.T) {
	pao, _ := managedSetup(t, "", "listen: 0.0.0.0:80\n")
	_, err := Load(pao)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen", "a setting it may not hold is an error, not ignored")

	pao, _ = managedSetup(t, "", "channels:\n  - id: 2\n    name: x\n    kind: bot\n")
	_, err = Load(pao)
	require.Error(t, err, "its channels are checked as pao.yaml's are")
	assert.Contains(t, err.Error(), "managed.yaml: channels[0]")
}

func TestTheManagedFileIsNeverPaoYamlItself(t *testing.T) {
	dir := t.TempDir()
	pao := filepath.Join(dir, "pao.yaml")
	write(t, pao, "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nmanaged: ./pao.yaml\n")
	_, err := Load(pao)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it is this file")

	require.NoError(t, os.Symlink(pao, filepath.Join(dir, "alias.yaml")))
	write(t, pao, "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\nmanaged: alias.yaml\n")
	_, err = Load(pao)
	require.Error(t, err, "not by another name either")
}

func TestAWriteThatLoadsReplacesTheManagedFileWhole(t *testing.T) {
	pao, mf := managedSetup(t, "channels:\n  - id: 1\n    name: mine\n"+chanOK, "")
	paoBefore, err := os.ReadFile(pao)
	require.NoError(t, err)

	m := Managed{Channels: []Channel{{ID: 2, Name: "ui-made", Kind: "bot", CredentialEnv: "PAO_UI_BOT",
		Groups: []Group{{ID: "-5", Name: "crew", Members: MembersOperators, Topics: []Topic{{ID: "7", Name: "crew-news"}}}}}}}
	require.NoError(t, WriteManaged(pao, m))

	b, err := os.ReadFile(mf)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), managedHeader), "it says it is pao's")
	st, err := os.Stat(mf)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	after, err := os.ReadFile(pao)
	require.NoError(t, err)
	assert.Equal(t, paoBefore, after, "pao.yaml is never written")

	cfg, err := Load(pao)
	require.NoError(t, err)
	require.Len(t, cfg.Orchestrator.Channels, 2)
	assert.Equal(t, m.Channels[0], cfg.Orchestrator.Channels[1], "what is written is what loads")

	entries, err := os.ReadDir(filepath.Dir(mf))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".managed.yaml."), "no partial file is left behind: %s", e.Name())
	}
}

func TestAWriteThatWouldNotLoadIsRefusedAndChangesNothing(t *testing.T) {
	pao, mf := managedSetup(t, "channels:\n  - id: 1\n    name: mine\n"+chanOK, "channels:\n  - id: 2\n    name: ui-made\n"+chanUI)
	before, err := os.ReadFile(mf)
	require.NoError(t, err)
	for name, m := range map[string]Managed{
		"clash with pao.yaml": {Channels: []Channel{{ID: 3, Name: "mine", Kind: "bot", CredentialEnv: "X"}}},
		"invalid channel":     {Channels: []Channel{{ID: 3, Name: "Bad Name", Kind: "bot", CredentialEnv: "X"}}},
	} {
		require.Error(t, WriteManaged(pao, m), name)
		after, err := os.ReadFile(mf)
		require.NoError(t, err)
		assert.Equal(t, before, after, name)
	}
}

// A reader of the managed file, as pao is at start, sees either the old file or
// the new one, never part of one.
func TestAReaderNeverSeesAHalfWrittenManagedFile(t *testing.T) {
	pao, mf := managedSetup(t, "", "")
	many := func(n int) Managed {
		var m Managed
		for i := 1; i <= n; i++ {
			m.Channels = append(m.Channels, Channel{ID: i, Name: "c" + strings.Repeat("x", i%20) + string(rune('a'+i%26)) + "-" + strings.Repeat("0", i/26), Kind: "bot", CredentialEnv: fmt.Sprintf("V%d", i)})
		}
		return m
	}
	require.NoError(t, WriteManaged(pao, many(1)))
	var stop atomic.Bool
	var reads, bad atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				var m Managed
				if err := decodeFile(mf, &m); err != nil || (len(m.Channels) != 1 && len(m.Channels) != 200) {
					bad.Add(1)
				}
				reads.Add(1)
			}
		}()
	}
	for i := range 40 {
		require.NoError(t, WriteManaged(pao, many([]int{1, 200}[i%2])))
	}
	stop.Store(true)
	wg.Wait()
	assert.Positive(t, reads.Load())
	assert.Zero(t, bad.Load(), "of %d reads", reads.Load())
}
