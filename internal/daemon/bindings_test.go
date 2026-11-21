package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// bindingRig is a binding set over a layer with the local channel and a channel
// "chat", for the configured agents bee and wasp, the admitted group team and the
// subject alice.
type bindingRig struct {
	dir   string
	cfg   *config.Config
	layer *comm.Layer
	local *recChannel
	chat  *recChannel
	set   *bindingSet
}

func newBindingRig(t *testing.T, configured ...config.Binding) *bindingRig {
	t.Helper()
	dir := t.TempDir()
	reg := registry.New()
	st, err := msgstore.Open(filepath.Join(dir, "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	r := &bindingRig{dir: dir, local: &recChannel{name: "local"}, chat: &recChannel{name: "chat"}}
	r.layer = comm.New(st, route.New(reg, nil, nil))
	r.layer.AddChannel(r.local)
	r.layer.AddChannel(r.chat)
	r.cfg = &config.Config{
		Orchestrator: config.Orchestrator{Level: 2, Bindings: configured,
			Channels: []config.Channel{{Name: "chat", Groups: []config.Group{{ID: "-5", Name: "team", Members: config.MembersOperators}}}}},
		Profiles: []core.Profile{{Name: "bee"}, {Name: "wasp"}},
	}
	r.set = r.load(t)
	return r
}

func (r *bindingRig) path() string { return filepath.Join(r.dir, "bindings.json") }

func (r *bindingRig) load(t *testing.T) *bindingSet {
	t.Helper()
	s, err := loadBindingSet(r.path(), r.cfg, r.layer, func(name string) bool { return name == "alice" })
	require.NoError(t, err)
	return s
}

// notice sends pao's message about bee and returns which channels were handed it.
func (r *bindingRig) notice(t *testing.T) []string {
	t.Helper()
	r.local.got, r.chat.got = nil, nil
	_, err := r.layer.NotifyAgent(context.Background(), "bee", core.Message{Sender: "pao", Text: "n"})
	require.NoError(t, err)
	var out []string
	for _, c := range []*recChannel{r.local, r.chat} {
		if len(c.texts()) > 0 {
			out = append(out, c.name)
		}
	}
	return out
}

func TestABindingSetAtRuntimeIsRoutedListedAndKept(t *testing.T) {
	r := newBindingRig(t, config.Binding{Agent: "bee", Stream: "stdout", To: "group:team", Level: 2})
	assert.Equal(t, []string{"local", "chat"}, r.notice(t), "no stderr binding: the default")

	b, err := r.set.add(config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"})
	require.NoError(t, err)
	assert.Equal(t, config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"}, b)
	assert.Equal(t, []string{"local"}, r.notice(t), "the layer routes by it at once")

	_, err = r.set.add(config.Binding{Agent: "wasp", To: "private:alice"})
	require.NoError(t, err)
	assert.Equal(t, []listedBinding{
		{config.Binding{Agent: "bee", Stream: "stdout", To: "group:team", Level: 2}, "config"},
		{config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"}, "runtime"},
		{config.Binding{Agent: "wasp", Stream: "stdout", To: "private:alice", Level: 2}, "runtime"},
	}, r.set.list(""), "the config's first, then the runtime's, defaults filled in")
	assert.Len(t, r.set.list("wasp"), 1)

	r.layer.SetBindings(nil)
	again := r.load(t)
	assert.Equal(t, r.set.list(""), again.list(""), "kept across a restart")
	assert.Equal(t, []string{"local"}, r.notice(t), "and given to the layer on load")
}

func TestARuntimeBindingIsRemovedAndAConfigBindingIsNot(t *testing.T) {
	r := newBindingRig(t, config.Binding{Agent: "bee", Stream: "stdout", To: "group:team", Level: 2})
	_, err := r.set.add(config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"})
	require.NoError(t, err)

	_, err = r.set.remove("bee", "stdout", "group:team")
	require.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), "by the config")

	_, err = r.set.remove("bee", "", "channel:local")
	require.ErrorIs(t, err, local.ErrNotFound, "an empty stream is stdout, and bee has no stdout binding to local")

	gone, err := r.set.remove("bee", "stderr", "channel:local")
	require.NoError(t, err)
	assert.Equal(t, "channel:local", gone.To)
	assert.Equal(t, []string{"local", "chat"}, r.notice(t), "the default again")
	assert.Len(t, r.load(t).list(""), 1, "and gone from the state file")
}

func TestABindingSetAtRuntimeIsCheckedAsTheConfigChecksOne(t *testing.T) {
	r := newBindingRig(t, config.Binding{Agent: "bee", Stream: "stdout", To: "group:team", Level: 2})
	for name, c := range map[string]struct {
		b    config.Binding
		want error
	}{
		"config duplicate":  {config.Binding{Agent: "bee", To: "group:team"}, local.ErrConflict},
		"unknown agent":     {config.Binding{Agent: "moth", To: "group:team"}, local.ErrBadRequest},
		"unknown group":     {config.Binding{Agent: "bee", To: "group:other"}, local.ErrBadRequest},
		"unknown subject":   {config.Binding{Agent: "bee", To: "private:bob"}, local.ErrBadRequest},
		"bad destination":   {config.Binding{Agent: "bee", To: "room:x"}, local.ErrBadRequest},
		"stderr with level": {config.Binding{Agent: "bee", Stream: "stderr", To: "private:alice", Level: 3}, local.ErrBadRequest},
		"long prefix":       {config.Binding{Agent: "bee", To: "private:alice", Prefix: "12345678901"}, local.ErrBadRequest},
	} {
		_, err := r.set.add(c.b)
		assert.ErrorIs(t, err, c.want, name)
	}
	_, err := r.set.add(config.Binding{Agent: "bee", To: "private:alice"})
	require.NoError(t, err)
	_, err = r.set.add(config.Binding{Agent: "bee", To: "private:alice", Level: 4})
	assert.ErrorIs(t, err, local.ErrConflict, "a runtime duplicate, whatever its level")
	_, err = r.set.add(config.Binding{Agent: "bee", Stream: "stderr", To: "private:alice"})
	assert.NoError(t, err, "the other stream to the same destination")
	assert.Len(t, r.set.list(""), 3)
}

func TestABindingThatCannotBeKeptIsNotSet(t *testing.T) {
	r := newBindingRig(t)
	r.set.path = filepath.Join(r.dir, "missing", "bindings.json")
	_, err := r.set.add(config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"})
	require.Error(t, err)
	assert.Empty(t, r.set.list(""))
	assert.Equal(t, []string{"local", "chat"}, r.notice(t), "nor routed")
}

func TestASavedBindingThatNoLongerChecksIsDroppedOnLoad(t *testing.T) {
	r := newBindingRig(t)
	require.NoError(t, os.WriteFile(r.path(), []byte(`[
		{"Agent":"gone","Stream":"stdout","To":"group:team","Level":2},
		{"Agent":"bee","Stream":"stderr","To":"channel:local"},
		{"Agent":"bee","Stream":"stdout","To":"group:dropped","Level":2}
	]`), 0o600))
	s := r.load(t)
	assert.Equal(t, []listedBinding{{config.Binding{Agent: "bee", Stream: "stderr", To: "channel:local"}, "runtime"}}, s.list(""))
	assert.Equal(t, []string{"local"}, r.notice(t))
	saved, err := os.ReadFile(r.path())
	require.NoError(t, err)
	assert.JSONEq(t, `[{"Agent":"bee","Stream":"stderr","To":"channel:local","Level":0,"Prefix":""}]`, string(saved), "the file is rewritten without them")
}

func TestAnUnreadableBindingsFileStopsTheNode(t *testing.T) {
	r := newBindingRig(t)
	require.NoError(t, os.WriteFile(r.path(), []byte(`{not json`), 0o600))
	_, err := loadBindingSet(r.path(), r.cfg, r.layer, func(string) bool { return true })
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}

func TestALevelIsReadAndWrittenAsTheConfigWritesIt(t *testing.T) {
	for text, level := range map[string]config.BindingLevel{"": 0, "3": 3, "log": config.BindingLevel(core.LevelLog)} {
		got, err := parseLevel(text)
		require.NoError(t, err, text)
		assert.Equal(t, level, got, text)
		assert.Equal(t, text, levelText(level), text)
	}
	_, err := parseLevel("loud")
	assert.Error(t, err)
}

func TestTheGovernorTakesNoBindingAtRuntime(t *testing.T) {
	r := newBindingRig(t)
	r.cfg.Orchestrator.Governor = config.Governor{Agent: "bee"}
	r.set = r.load(t)
	_, err := r.set.add(config.Binding{Agent: "bee", To: "private:alice"})
	require.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), "is the governor")
	_, err = r.set.add(config.Binding{Agent: "wasp", To: "private:alice"})
	assert.NoError(t, err, "another agent still may be bound")
}
