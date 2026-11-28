package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type fake struct {
	name string
	spec Spec
	h    Handler
}

func (f *fake) Name() string                             { return f.name }
func (f *fake) Send(context.Context, core.Message) error { return nil }
func (f *fake) Run(context.Context) error                { return nil }

func okFactory(spec Spec, h Handler) (Adapter, error) {
	return &fake{name: spec.Name, spec: spec, h: h}, nil
}

type nilHandler struct{}

func (nilHandler) ReceiveFromChannel(context.Context, comm.ChannelInbound) (comm.ChannelResult, error) {
	return comm.ChannelResult{}, nil
}
func (nilHandler) Destinations(string, core.Message) []string   { return nil }
func (nilHandler) NativeOf(string, string) (string, bool)       { return "", false }
func (nilHandler) DefaultAgent(string) (string, bool)           { return "", false }
func (nilHandler) DefaultAgentIn(string, string) (string, bool) { return "", false }

func TestTheTableListsKindsSorted(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, Table{"b": {New: okFactory}, "a": {New: okFactory}}.Kinds())
}

func TestBuildMakesOneAdapterPerSpecWithItsSpecAndHandler(t *testing.T) {
	h := nilHandler{}
	got, err := Table{"bot": {New: okFactory}}.Build([]Spec{
		{ID: 1, Name: "chat", Kind: "bot", Credential: "tok", Options: map[string]any{"x": 1}},
		{ID: 2, Name: "alt", Kind: "bot", Credential: "tok2"},
	}, h)
	require.NoError(t, err)
	require.Len(t, got, 2)
	f := got[0].(*fake)
	assert.Equal(t, Spec{ID: 1, Name: "chat", Kind: "bot", Credential: "tok", Options: map[string]any{"x": 1}}, f.spec)
	assert.Equal(t, h, f.h)
	assert.Equal(t, "alt", got[1].Name())
}

func TestAKindNotCompiledInIsAStartupErrorThatNamesWhatIs(t *testing.T) {
	_, err := Table{"bot": {New: okFactory}}.Build([]Spec{{ID: 1, Name: "chat", Kind: "nope", Credential: "x"}}, nilHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope" is not compiled into this build`)
	assert.Contains(t, err.Error(), "bot")
}

func TestAnEmptyCredentialStopsTheNodeAndTheErrorNeverHoldsIt(t *testing.T) {
	_, err := Table{"bot": {New: okFactory}}.Build([]Spec{{ID: 1, Name: "chat", Kind: "bot", Credential: ""}}, nilHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential")

	_, err = Table{"bot": {New: func(s Spec, _ Handler) (Adapter, error) { return nil, errors.New("rejected") }}}.Build(
		[]Spec{{ID: 1, Name: "chat", Kind: "bot", Credential: "SECRET-TOKEN"}}, nilHandler{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-TOKEN")
	assert.Contains(t, err.Error(), `channel "chat"`)
}

func TestAnAdapterThatRenamesItselfIsRefused(t *testing.T) {
	_, err := Table{"bot": {New: func(s Spec, h Handler) (Adapter, error) { return &fake{name: "other"}, nil }}}.Build(
		[]Spec{{ID: 1, Name: "chat", Kind: "bot", Credential: "x"}}, nilHandler{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named itself")
}

func TestCheckReportsEveryFailingProviderWithoutACredential(t *testing.T) {
	tbl := Table{
		"bot": {New: okFactory, Check: func(o map[string]any) error {
			if _, bad := o["bad"]; bad {
				return errors.New("bad option")
			}
			return nil
		}},
		"bare": {New: okFactory},
	}
	assert.NoError(t, tbl.Check([]Spec{{ID: 1, Name: "chat", Kind: "bot"}}), "no credential is needed")

	err := tbl.Check([]Spec{
		{ID: 1, Name: "one", Kind: "bot", Options: map[string]any{"bad": 1}},
		{ID: 2, Name: "two", Kind: "nope"},
		{ID: 3, Name: "three", Kind: "bare"},
		{ID: 4, Name: "four", Kind: "bot"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "one": bad option`)
	assert.Contains(t, err.Error(), `provider "two": kind "nope" is not compiled into this build`)
	assert.Contains(t, err.Error(), `provider "three": kind "bare" has no option check`)
	assert.NotContains(t, err.Error(), `"four"`)
}

func ledgerPath(t *testing.T) string { return filepath.Join(t.TempDir(), "channels.json") }

func TestALedgerAdmitsNewChannelsAndRemembersThemAcrossRestarts(t *testing.T) {
	p := ledgerPath(t)
	l, err := OpenLedger(p)
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}, {ID: 2, Name: "alt"}}))

	l, err = OpenLedger(p)
	require.NoError(t, err)
	assert.Equal(t, map[int]string{1: "chat", 2: "alt"}, l.ids)
	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestARemovedChannelsIdStaysRetiredAndCannotGoToAnotherChannel(t *testing.T) {
	p := ledgerPath(t)
	l, err := OpenLedger(p)
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}, {ID: 2, Name: "alt"}}))

	l, err = OpenLedger(p)
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 2, Name: "alt"}}), "channel 1 removed: fine")

	err = l.Admit([]Spec{{ID: 1, Name: "newchat"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never reused")

	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}}), "the same channel may come back with its id")
}

func TestAServedNameKeepsItsIdAndAChannelKeepsItsName(t *testing.T) {
	l, err := OpenLedger(ledgerPath(t))
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}}))
	err = l.Admit([]Spec{{ID: 7, Name: "chat"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keeps its id")
	err = l.Admit([]Spec{{ID: 1, Name: "renamed"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keeps its name")
}

func TestARefusedConfigurationWritesNothing(t *testing.T) {
	p := ledgerPath(t)
	l, err := OpenLedger(p)
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}}))
	before, err := os.ReadFile(p)
	require.NoError(t, err)

	require.Error(t, l.Admit([]Spec{{ID: 5, Name: "fresh"}, {ID: 1, Name: "clash"}}))
	after, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the good channel in a bad set was not recorded")
	assert.Equal(t, map[int]string{1: "chat"}, l.ids)
}

func TestAnUnchangedSetDoesNotRewriteTheFileAndABadFileIsAnError(t *testing.T) {
	p := ledgerPath(t)
	l, err := OpenLedger(p)
	require.NoError(t, err)
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}}))
	require.NoError(t, os.Remove(p))
	require.NoError(t, l.Admit([]Spec{{ID: 1, Name: "chat"}}))
	_, err = os.Stat(p)
	assert.True(t, os.IsNotExist(err), "nothing new, nothing written")

	for _, body := range []string{"{not json", `{"channels":{"x":"chat"}}`, `{"channels":{"0":"chat"}}`, `{"channels":{"1":""}}`} {
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		_, err := OpenLedger(p)
		assert.Error(t, err, body)
	}
}
