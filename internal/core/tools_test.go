package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAProfilesOwnToolsWinOverTheDefaultsAndTheDefaultIsNotShared(t *testing.T) {
	def := []string{"AskUserQuestion"}
	d := Tools{Disable: &def}

	got := Profile{}.WithToolDefaults(d)
	assert.Equal(t, []string{"AskUserQuestion"}, got.DisabledTools(), "unset: the default")
	(*got.Tools.Disable)[0] = "changed"
	assert.Equal(t, "AskUserQuestion", def[0], "each profile gets its own copy")

	own := []string{"Bash"}
	assert.Equal(t, []string{"Bash"}, Profile{Tools: Tools{Disable: &own}}.WithToolDefaults(d).DisabledTools())
	empty := []string{}
	assert.Empty(t, Profile{Tools: Tools{Disable: &empty}}.WithToolDefaults(d).DisabledTools(), "an empty list turns the default off")
	assert.Nil(t, Profile{}.WithToolDefaults(Tools{}).Tools.Disable, "no default, nothing set")
}
