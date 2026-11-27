package shell

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAQuotedWordReachesTheProgramAsOneArgument(t *testing.T) {
	for _, s := range []string{"", "plain", "two words", "it's", `a"b`, "$HOME `x` \\ \n *"} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+Quote(s)).Output()
		require.NoError(t, err)
		assert.Equal(t, s, string(out))
	}
}
