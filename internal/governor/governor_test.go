package governor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
)

func TestTheInstructionsListEveryProposableCommandAndNoOther(t *testing.T) {
	got := Instructions("/gpao", nil)
	for _, p := range command.Proposals(nil) {
		assert.Contains(t, got, "- `"+p.Usage+"`: "+p.About)
	}
	for _, w := range []string{"help", "allow", "always", "deny"} {
		assert.NotContains(t, got, "`/pao "+w, w)
	}
	assert.Contains(t, got, "`/pao spawn <recipe> [name]`")
	assert.Contains(t, got, "an operator sent with `/gpao`")
	assert.NotContains(t, got, "{")
	assert.Contains(t, got, "do not answer the question yourself", "a question is translated too, not answered")
	assert.Contains(t, got, "or in the group they wrote in, to everyone there", "it is told its answer may be read by a whole group")
}

func TestTheInstructionsFollowTheNodesPrefixAndNarrowerSet(t *testing.T) {
	got := Instructions("/ask", []string{"agents", "view"})
	assert.Contains(t, got, "an operator sent with `/ask`")
	assert.NotContains(t, got, "/gpao")
	cmds := got[strings.Index(got, "## Commands you may propose"):]
	assert.Equal(t, 2, strings.Count(cmds, "\n- `/pao "), cmds)
	assert.Contains(t, cmds, "`/pao agents`")
	assert.Contains(t, cmds, "`/pao view <agent> pause|resume`")
}
