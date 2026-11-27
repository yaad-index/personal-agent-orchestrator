package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

const okEndpoint = "https://bot.example/bot{token}/{method}"

// channelNode writes a node with one channel of its own in pao.yaml and, when
// managed is set, names managed.yaml; secret.txt holds a secret's value.
func channelNode(t *testing.T, managed bool) (pao, mf string) {
	t.Helper()
	dir := t.TempDir()
	pao, mf = filepath.Join(dir, "pao.yaml"), filepath.Join(dir, "managed.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("FILE-SECRET-VALUE\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o600))
	m := ""
	if managed {
		m = "managed: managed.yaml\n"
	}
	require.NoError(t, os.WriteFile(pao, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+m+
		"secrets:\n  - name: in-file\n    file: secret.txt\n  - name: in-var\n    env: S_VAR\n  - name: blank\n    file: empty.txt\n"+
		"channels:\n  - id: 1\n    name: mine\n    kind: bot\n    credential_env: MINE_VAR\n    options:\n      endpoint: \""+okEndpoint+"\"\n"), 0o600))
	return pao, mf
}

func channelAPI(t *testing.T, pao string, env map[string]string) *channelConfigAPI {
	t.Helper()
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	return &channelConfigAPI{
		path: pao, running: cfg.Orchestrator.Channels, adapters: builtin.Table(),
		startEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
	}
}

func uiChannel(id int, name string) local.ChannelConfigJSON {
	return local.ChannelConfigJSON{ID: id, Name: name, Kind: "bot", CredentialSecret: "in-file", Options: map[string]any{"endpoint": okEndpoint}}
}

func TestTheChannelsAreListedAsTheNextStartWouldHaveThem(t *testing.T) {
	pao, _ := channelNode(t, true)
	t.Setenv("S_VAR", "set-now-but-not-at-start")
	a := channelAPI(t, pao, map[string]string{"MINE_VAR": "ENV-SECRET-VALUE"})
	ctx := context.Background()

	l, err := a.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(pao), "managed.yaml"), l.ManagedFile)
	assert.Equal(t, []string{"bot"}, l.Kinds)
	assert.Equal(t, []string{"in-file", "in-var", "blank"}, l.Secrets)
	require.Len(t, l.Channels, 1)
	assert.False(t, l.Channels[0].Managed)
	assert.True(t, l.Channels[0].Applied)
	assert.True(t, l.Channels[0].Credential.Resolves)
	assert.False(t, l.RestartNeeded)

	for _, c := range []local.ChannelConfigJSON{uiChannel(2, "via-file"), {ID: 3, Name: "via-var", Kind: "bot", CredentialSecret: "in-var", Options: map[string]any{"endpoint": okEndpoint}},
		{ID: 4, Name: "via-blank", Kind: "bot", CredentialSecret: "blank", Options: map[string]any{"endpoint": okEndpoint}},
		{ID: 5, Name: "unset", Kind: "bot", CredentialEnv: "NOT_SET_VAR", Options: map[string]any{"endpoint": okEndpoint}}} {
		require.NoError(t, a.Put(ctx, c), c.Name)
	}
	l, err = a.List(ctx)
	require.NoError(t, err)
	byName := map[string]local.ChannelConfigEntryJSON{}
	for _, c := range l.Channels {
		byName[c.Name] = c
	}
	assert.True(t, l.RestartNeeded, "written, not yet running")
	assert.True(t, byName["via-file"].Managed)
	assert.False(t, byName["via-file"].Applied)
	assert.True(t, byName["via-file"].Credential.Resolves)
	assert.False(t, byName["via-var"].Credential.Resolves, "a variable set after start is not one pao starts with")
	assert.Contains(t, byName["via-var"].Credential.Problem, "S_VAR is not set in the environment pao was started with")
	assert.Contains(t, byName["via-blank"].Credential.Problem, "is empty")
	assert.Contains(t, byName["unset"].Credential.Problem, "NOT_SET_VAR")

	b, err := json.Marshal(l)
	require.NoError(t, err)
	for _, never := range []string{"FILE-SECRET-VALUE", "ENV-SECRET-VALUE", "set-now-but-not-at-start"} {
		assert.NotContains(t, string(b), never, "a credential's value is never in the answer")
	}
}

func TestOnlyTheManagedFilesChannelsAreChanged(t *testing.T) {
	pao, mf := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()
	paoBefore, err := os.ReadFile(pao)
	require.NoError(t, err)

	err = a.Put(ctx, local.ChannelConfigJSON{ID: 1, Name: "mine", Kind: "bot", CredentialEnv: "OTHER", Options: map[string]any{"endpoint": okEndpoint}})
	assert.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), "change it there")
	assert.ErrorIs(t, a.Delete(ctx, "mine"), local.ErrConflict)
	assert.ErrorIs(t, a.Delete(ctx, "nobody"), local.ErrNotFound)

	require.NoError(t, a.Put(ctx, uiChannel(2, "ui")))
	require.NoError(t, a.Delete(ctx, "ui"))
	l, err := a.List(ctx)
	require.NoError(t, err)
	assert.Len(t, l.Channels, 1)
	after, err := os.ReadFile(pao)
	require.NoError(t, err)
	assert.Equal(t, paoBefore, after, "pao.yaml is never written")
	_, err = os.Stat(mf)
	assert.NoError(t, err)
}

func TestAChangeThatWouldNotStartIsRefusedAndChangesNothing(t *testing.T) {
	pao, mf := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()
	require.NoError(t, a.Put(ctx, uiChannel(2, "ui")))
	before, err := os.ReadFile(mf)
	require.NoError(t, err)
	for name, c := range map[string]local.ChannelConfigJSON{
		"unknown option":  {ID: 3, Name: "x", Kind: "bot", CredentialEnv: "X_VAR", Options: map[string]any{"nonsense": 1}},
		"unknown kind":    {ID: 3, Name: "x", Kind: "carrier-pigeon", CredentialEnv: "X_VAR"},
		"same id":         {ID: 1, Name: "x", Kind: "bot", CredentialEnv: "X_VAR", Options: map[string]any{"endpoint": okEndpoint}},
		"shared variable": {ID: 3, Name: "x", Kind: "bot", CredentialEnv: "MINE_VAR", Options: map[string]any{"endpoint": okEndpoint}},
		"taken secret":    {ID: 3, Name: "x", Kind: "bot", CredentialSecret: "in-file", Options: map[string]any{"endpoint": okEndpoint}},
		"no credential":   {ID: 3, Name: "x", Kind: "bot", Options: map[string]any{"endpoint": okEndpoint}},
	} {
		err := a.Put(ctx, c)
		assert.ErrorIs(t, err, local.ErrBadRequest, name)
		after, rerr := os.ReadFile(mf)
		require.NoError(t, rerr)
		assert.Equal(t, before, after, name)
	}
}

func TestRedefiningAChannelKeepsItsGroupsTopics(t *testing.T) {
	pao, mf := channelNode(t, true)
	require.NoError(t, os.WriteFile(mf, []byte("channels:\n  - id: 2\n    name: ui\n    kind: bot\n    credential_env: UI_VAR\n    options:\n      endpoint: \""+okEndpoint+
		"\"\n    groups:\n      - id: \"-5\"\n        name: crew\n        members: operators\n        topics:\n          - id: \"7\"\n            name: crew-news\n"), 0o600))
	a := channelAPI(t, pao, nil)
	c := local.ChannelConfigJSON{ID: 2, Name: "ui", Kind: "bot", CredentialEnv: "UI_VAR2", Options: map[string]any{"endpoint": okEndpoint},
		Groups: []local.GroupConfigJSON{{ID: "-5", Name: "crew"}, {ID: "-6", Name: "new-crew"}}}
	require.NoError(t, a.Put(context.Background(), c))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	got := cfg.Orchestrator.Channels[1]
	assert.Equal(t, "UI_VAR2", got.CredentialEnv)
	require.Len(t, got.Groups, 2)
	assert.Equal(t, []config.Topic{{ID: "7", Name: "crew-news"}}, got.Groups[0].Topics, "the form does not show topics, so it does not drop them")
	assert.Equal(t, config.MembersOperators, got.Groups[1].Members)
}

func TestWithoutAManagedFileNothingChanges(t *testing.T) {
	pao, mf := channelNode(t, false)
	a := channelAPI(t, pao, nil)
	l, err := a.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, l.ManagedFile)
	err = a.Put(context.Background(), uiChannel(2, "ui"))
	assert.ErrorIs(t, err, local.ErrConflict)
	assert.ErrorIs(t, err, config.ErrNotManaged)
	_, err = os.Stat(mf)
	assert.True(t, os.IsNotExist(err))
}

func TestConcurrentChangesAreNotLost(t *testing.T) {
	pao, _ := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := local.ChannelConfigJSON{ID: 10 + i, Name: fmt.Sprintf("c%d", i), Kind: "bot", CredentialEnv: fmt.Sprintf("C%d_VAR", i), Options: map[string]any{"endpoint": okEndpoint}}
			assert.NoError(t, a.Put(context.Background(), c))
		}()
	}
	wg.Wait()
	l, err := a.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, l.Channels, 9, "each change read the file the one before it wrote")
}

func writeLedger(t *testing.T, pao, body string) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(pao), "state")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channels.json"), []byte(body), 0o600))
}

func TestANewChannelIsGivenAnIdNeverUsed(t *testing.T) {
	pao, _ := channelNode(t, true)
	writeLedger(t, pao, `{"channels":{"1":"mine","7":"retired","4":"comeback"}}`)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()
	c := uiChannel(0, "fresh")
	require.NoError(t, a.Put(ctx, c))
	back := uiChannel(0, "comeback")
	back.CredentialSecret, back.CredentialEnv = "", "BACK_VAR"
	require.NoError(t, a.Put(ctx, back))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	ids := map[string]int{}
	for _, ch := range cfg.Orchestrator.Channels {
		ids[ch.Name] = ch.ID
	}
	assert.Equal(t, 8, ids["fresh"], "above every id served, retired ones included")
	assert.Equal(t, 4, ids["comeback"], "a name served before gets its id back")

	reuse := local.ChannelConfigJSON{ID: 7, Name: "squatter", Kind: "bot", CredentialEnv: "SQ_VAR", Options: map[string]any{"endpoint": okEndpoint}}
	err = a.Put(ctx, reuse)
	assert.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), "never reused")
}

func TestARestartIsRefusedWhenPaoWouldNotStart(t *testing.T) {
	pao, mf := channelNode(t, true)
	env := map[string]string{"PAO_TOKEN": "t", "MINE_VAR": "v"}
	a := channelAPI(t, pao, env)
	require.NoError(t, a.RestartCheck(), "a node that would start")

	require.NoError(t, a.Put(context.Background(), local.ChannelConfigJSON{ID: 2, Name: "unset", Kind: "bot", CredentialEnv: "UNSET_VAR", Options: map[string]any{"endpoint": okEndpoint}}))
	err := a.RestartCheck()
	require.Error(t, err, "saved, but its credential is not there to read")
	assert.Contains(t, err.Error(), `channel "unset": variable UNSET_VAR is not set`)
	env["UNSET_VAR"] = "now-set"
	require.NoError(t, a.RestartCheck())

	writeLedger(t, pao, `{"channels":{"1":"mine","2":"someone-else"}}`)
	err = a.RestartCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never reused")
	writeLedger(t, pao, `{"channels":{"1":"mine"}}`)

	delete(env, "PAO_TOKEN")
	err = a.RestartCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token_env")
	env["PAO_TOKEN"] = "t"

	require.NoError(t, os.WriteFile(mf, []byte("listen: 0.0.0.0:1\n"), 0o600))
	err = a.RestartCheck()
	require.Error(t, err, "a file edited by hand into one that does not load")
	assert.Contains(t, err.Error(), "pao would not start")
}
