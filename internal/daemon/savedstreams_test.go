package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// restart is a node started again on the rig's state: a new layer state, the same
// state directory.
func (r *streamRig) restart(t *testing.T) *streamControl {
	t.Helper()
	r.layer.Unbind("bee")
	saved, err := loadSavedStreams(filepath.Join(r.dir, "streams.json"))
	require.NoError(t, err)
	sc := &streamControl{layer: r.layer, reg: r.reg, views: newViews(), level: 1, saved: saved, send: r.sc.send}
	sc.restore()
	return sc
}

// A stream binding outlives a restart of the node (ADR-0026 point 8); a pause
// does not.
func TestAStreamBindingIsKeptAcrossARestart(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, operatorViewer))
	require.NoError(t, r.sc.Pause("bee", operatorViewer))

	sc := r.restart(t)
	ch, ok := r.layer.Bound("bee")
	require.True(t, ok)
	assert.Equal(t, "chat", ch)
	level, viewer := sc.views.info("bee")
	assert.Equal(t, 3, level)
	assert.Equal(t, operatorViewer, viewer)
	assert.False(t, sc.views.hold("bee", core.Message{Text: "x"}), "a pause is not kept")
}

func TestClearingAStreamForgetsItAcrossARestart(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, operatorViewer))
	require.NoError(t, r.sc.SetStream("bee", "", 0, operatorViewer))
	r.restart(t)
	_, ok := r.layer.Bound("bee")
	assert.False(t, ok)
	assert.Empty(t, r.sc.saved.all())
}

func TestLettingABindingGoForgetsItAcrossARestart(t *testing.T) {
	r := newStreamRig(t)
	require.NoError(t, r.sc.SetStream("bee", "chat", 3, operatorViewer))
	bindings{r.layer, r.sc.views, r.sc.saved}.Unbind("bee")
	r.restart(t)
	_, ok := r.layer.Bound("bee")
	assert.False(t, ok, "a temporary agent's name spawned again does not inherit the binding")
}

func TestARestoreDropsABindingWhoseAgentOrChannelIsGone(t *testing.T) {
	r := newStreamRig(t)
	path := filepath.Join(r.dir, "streams.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"bee":{"channel":"gone"},"ghost":{"channel":"chat"}}`), 0o600))
	r.restart(t)
	_, ok := r.layer.Bound("bee")
	assert.False(t, ok)
	saved, err := loadSavedStreams(path)
	require.NoError(t, err)
	assert.Empty(t, saved.all(), "both are dropped from the file too")
}

// A binding that cannot be kept is not made: what the node does and what it would
// do after a restart do not part.
func TestABindingThatCannotBeKeptIsNotMade(t *testing.T) {
	r := newStreamRig(t)
	r.sc.saved.path = filepath.Join(r.dir, "missing", "streams.json")
	err := r.sc.SetStream("bee", "chat", 3, operatorViewer)
	require.Error(t, err)
	_, ok := r.layer.Bound("bee")
	assert.False(t, ok)
	assert.Empty(t, r.sc.saved.all())
}

func TestABindingToAChannelThatDoesNotExistIsRefusedAndNotKept(t *testing.T) {
	r := newStreamRig(t)
	err := r.sc.SetStream("bee", "nope", 3, operatorViewer)
	require.ErrorIs(t, err, temp.ErrBadRequest)
	assert.Contains(t, err.Error(), `no channel "nope"`)
	assert.Empty(t, r.sc.saved.all())
}

func TestACorruptBindingsFileStopsTheStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streams.json")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	_, err := loadSavedStreams(path)
	assert.Error(t, err)
}

// The chat commands run the operator surface's own operations (ADR-0026 point 2).
func TestChatCommandsRunTheOperatorsOperations(t *testing.T) {
	r := newStreamRig(t)
	c := chatCommands{streams: &streamAPI{streams: r.sc}}
	require.NoError(t, c.SetStream(context.Background(), "bee", "chat", 2))
	ch, ok := r.layer.Bound("bee")
	require.True(t, ok)
	assert.Equal(t, "chat", ch)
	_, viewer := r.sc.views.info("bee")
	assert.Equal(t, operatorViewer, viewer, "a chat command acts as the operator's view")

	require.NoError(t, c.View(context.Background(), "bee", "pause"))
	assert.True(t, r.sc.views.hold("bee", core.Message{Text: "x"}))
	require.NoError(t, c.View(context.Background(), "bee", "resume"))

	err := c.SetStream(context.Background(), "ghost", "chat", 2)
	require.Error(t, err)
	assert.Equal(t, `no agent "ghost"`, err.Error(), "the operator surface's own words")
}
