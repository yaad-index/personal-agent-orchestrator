package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

const okEndpoint = "https://bot.example/bot{token}/{method}"

// channelNode writes a node whose pao.yaml defines the channel mine on the
// provider mine, and the providers a test puts channels on: via-file,
// via-var and via-blank, whose credentials are secrets, unset, whose variable
// pao was not started with, and free1 to free9. When managed is set, it
// names managed.yaml. secret.txt holds a secret's value.
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
	provider := func(name, cred string) string {
		return "  - name: " + name + "\n    kind: bot\n    " + cred + "\n    options:\n      endpoint: \"" + okEndpoint + "\"\n"
	}
	provs := provider("mine", "credential_env: MINE_VAR") + provider("via-file", "credential_secret: in-file") +
		provider("via-var", "credential_secret: in-var") + provider("via-blank", "credential_secret: blank") +
		provider("unset", "credential_env: NOT_SET_VAR")
	for i := 1; i <= 9; i++ {
		provs += provider(fmt.Sprintf("free%d", i), fmt.Sprintf("credential_env: FREE%d_VAR", i))
	}
	require.NoError(t, os.WriteFile(pao, []byte("node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"+m+
		"secrets:\n  - name: in-file\n    file: secret.txt\n  - name: in-var\n    env: S_VAR\n  - name: blank\n    file: empty.txt\n"+
		"providers:\n"+provs+"channels:\n  - id: 1\n    name: mine\n    provider: mine\n    owner: alice\n    target: private\n"), 0o600))
	return pao, mf
}

func channelAPI(t *testing.T, pao string, env map[string]string) *channelConfigAPI {
	t.Helper()
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	return &channelConfigAPI{
		path: pao, running: cfg.Orchestrator.Channels, adapters: builtin.Table(),
		startEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
		codes:    newIdentifyCodes(time.Now),
	}
}

// putIdentified puts c with a code issued for its target, as if Identify had
// sent it and the operator had entered it.
func putIdentified(ctx context.Context, a *channelConfigAPI, c local.ChannelConfigJSON) error {
	code, _, err := a.codes.issue(c.Name, where{c.Provider, c.Target, c.Owner})
	if err != nil {
		return err
	}
	c.Code = code
	return a.Put(ctx, c)
}

func uiChannel(id int, name, provider string) local.ChannelConfigJSON {
	return local.ChannelConfigJSON{ID: id, Name: name, Provider: provider, Owner: "alice", Target: config.TargetPrivate}
}

func TestTheChannelsAndProvidersAreListedAsTheNextStartWouldHaveThem(t *testing.T) {
	pao, _ := channelNode(t, true)
	t.Setenv("S_VAR", "set-now-but-not-at-start")
	a := channelAPI(t, pao, map[string]string{"MINE_VAR": "ENV-SECRET-VALUE"})
	ctx := context.Background()
	l, err := a.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(filepath.Dir(pao), "managed.yaml"), l.ManagedFile)
	require.Len(t, l.Channels, 1)
	assert.Equal(t, "mine", l.Channels[0].Provider)
	assert.False(t, l.Channels[0].Managed)
	assert.True(t, l.Channels[0].Applied)
	assert.False(t, l.RestartNeeded)

	for _, c := range []local.ChannelConfigJSON{uiChannel(2, "a", "via-file"), uiChannel(3, "b", "via-var")} {
		require.NoError(t, putIdentified(ctx, a, c), c.Name)
	}
	l, err = a.List(ctx)
	require.NoError(t, err)
	byName := map[string]local.ChannelConfigEntryJSON{}
	for _, c := range l.Channels {
		byName[c.Name] = c
	}
	assert.True(t, l.RestartNeeded, "written, not yet running")
	assert.True(t, byName["a"].Managed)
	assert.False(t, byName["a"].Applied)

	prov := map[string]local.ProviderJSON{}
	for _, p := range l.Providers {
		prov[p.Name] = p
	}
	assert.Len(t, l.Providers, 14)
	assert.Equal(t, local.ProviderJSON{Name: "mine", Kind: "bot", Credential: local.CredentialStatusJSON{Resolves: true}, Channels: []string{"mine"}}, prov["mine"])
	assert.True(t, prov["via-file"].Credential.Resolves)
	assert.Equal(t, []string{"a"}, prov["via-file"].Channels)
	assert.False(t, prov["via-var"].Credential.Resolves, "a variable set after start is not one pao starts with")
	assert.Contains(t, prov["via-var"].Credential.Problem, "S_VAR is not set in the environment pao was started with")
	assert.Contains(t, prov["via-blank"].Credential.Problem, "is empty")
	assert.Contains(t, prov["unset"].Credential.Problem, "NOT_SET_VAR")
	assert.Empty(t, prov["free1"].Channels)

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

	err = putIdentified(ctx, a, uiChannel(1, "mine", "free1"))
	assert.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), "change it there")
	assert.ErrorIs(t, a.Delete(ctx, "mine"), local.ErrConflict)
	assert.ErrorIs(t, a.Delete(ctx, "nobody"), local.ErrNotFound)

	require.NoError(t, putIdentified(ctx, a, uiChannel(2, "ui", "free1")))
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
	require.NoError(t, putIdentified(ctx, a, uiChannel(2, "ui", "free1")))
	before, err := os.ReadFile(mf)
	require.NoError(t, err)
	for name, c := range map[string]local.ChannelConfigJSON{
		"no provider":      uiChannel(3, "x", ""),
		"unknown provider": uiChannel(3, "x", "nope"),
		"a carried one":    uiChannel(3, "x", "mine"),
		"same id":          uiChannel(1, "x", "free2"),
		"no owner":         {ID: 3, Name: "x", Provider: "free2", Target: config.TargetPrivate},
	} {
		err := putIdentified(ctx, a, c)
		assert.ErrorIs(t, err, local.ErrBadRequest, name)
		after, rerr := os.ReadFile(mf)
		require.NoError(t, rerr)
		assert.Equal(t, before, after, name)
	}
}

func TestRedefiningAChannelKeepsItsGroupsTopics(t *testing.T) {
	pao, mf := channelNode(t, true)
	require.NoError(t, os.WriteFile(mf, []byte("channels:\n  - id: 2\n    name: ui\n    provider: free1\n    owner: alice\n    target: \"-5/7\"\n"+
		"    groups:\n      - id: \"-5\"\n        name: crew\n        members: operators\n        topics:\n          - id: \"7\"\n            name: crew-news\n"), 0o600))
	a := channelAPI(t, pao, nil)
	c := local.ChannelConfigJSON{ID: 2, Name: "ui", Provider: "free2", Owner: "alice", Target: "-5/7",
		Groups: []local.GroupConfigJSON{{ID: "-5", Name: "crew"}}}
	require.NoError(t, putIdentified(context.Background(), a, c))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	got := cfg.Orchestrator.Channels[1]
	assert.Equal(t, "free2", got.Provider)
	assert.Equal(t, "-5/7", got.Target)
	require.Len(t, got.Groups, 1)
	assert.Equal(t, []config.Topic{{ID: "7", Name: "crew-news"}}, got.Groups[0].Topics, "the form does not show topics, so it does not drop them")
	assert.Equal(t, config.MembersOperators, got.Groups[0].Members)
}

func TestWithoutAManagedFileNothingChanges(t *testing.T) {
	pao, mf := channelNode(t, false)
	a := channelAPI(t, pao, nil)
	l, err := a.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, l.ManagedFile)
	err = putIdentified(context.Background(), a, uiChannel(2, "ui", "free1"))
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
			assert.NoError(t, putIdentified(context.Background(), a, uiChannel(10+i, fmt.Sprintf("c%d", i), fmt.Sprintf("free%d", i+1))))
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
	require.NoError(t, putIdentified(ctx, a, uiChannel(0, "fresh", "free1")))
	require.NoError(t, putIdentified(ctx, a, uiChannel(0, "comeback", "free2")))
	cfg, err := config.Load(pao)
	require.NoError(t, err)
	ids := map[string]int{}
	for _, ch := range cfg.Orchestrator.Channels {
		ids[ch.Name] = ch.ID
	}
	assert.Equal(t, 8, ids["fresh"], "above every id served, retired ones included")
	assert.Equal(t, 4, ids["comeback"], "a name served before gets its id back")

	reuse := uiChannel(7, "squatter", "free3")
	err = putIdentified(ctx, a, reuse)
	assert.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), "never reused")
}

func TestARestartIsRefusedWhenPaoWouldNotStart(t *testing.T) {
	pao, mf := channelNode(t, true)
	env := map[string]string{"PAO_TOKEN": "t", "MINE_VAR": "v"}
	a := channelAPI(t, pao, env)
	require.NoError(t, a.RestartCheck(), "a node that would start")

	require.NoError(t, putIdentified(context.Background(), a, uiChannel(2, "late", "unset")))
	err := a.RestartCheck()
	require.Error(t, err, "saved, but its provider's credential is not there to read")
	assert.Contains(t, err.Error(), `provider "unset": variable NOT_SET_VAR is not set`)
	env["NOT_SET_VAR"] = "now-set"
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
