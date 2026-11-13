package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVisibleByLevel(t *testing.T) {
	l := DefaultLevels()

	assert.True(t, l.Visible(KindMessage, 1))
	assert.False(t, l.Visible(KindToolCall, 1))
	assert.True(t, l.Visible(KindToolCall, 2))
	assert.False(t, l.Visible(KindReasoning, 2))
	assert.True(t, l.Visible(KindReasoning, 3))
	assert.False(t, l.Visible(KindOther, 3))
	assert.True(t, l.Visible(KindOther, 4))
}

func TestVisibleUnlistedKindNeverShows(t *testing.T) {
	l := Levels{KindMessage: 1}
	assert.False(t, l.Visible(KindToolCall, 99))
}

func TestLevelsValidate(t *testing.T) {
	assert.NoError(t, DefaultLevels().Validate())
	assert.ErrorContains(t, Levels{"gossip": 1}.Validate(), "unknown kind")
	assert.ErrorContains(t, Levels{KindMessage: 0}.Validate(), "at least 1")
}

func TestEveryKindHasADefaultLevel(t *testing.T) {
	l := DefaultLevels()
	for _, k := range Kinds {
		_, ok := l[k]
		assert.True(t, ok, "kind %q has no default level", k)
	}
}
