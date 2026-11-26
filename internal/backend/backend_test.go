package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// partial is an adapter that can shut out only some categories and owns one
// option.
type partial struct{ can []Category }

func (p partial) Isolates() []Category { return p.can }
func (partial) Owned(command string) []string {
	if strings.Contains(command, "--owned") {
		return []string{"--owned"}
	}
	return nil
}
func (partial) Command(p core.Profile, _ Launch) (Start, error) {
	return Start{Command: p.Startup.Command + " --isolated", Env: []string{"ISOLATED=1"}}, nil
}

func profile(backend string, inherit ...string) core.Profile {
	return core.Profile{Name: "a", Backend: backend, Startup: core.Startup{Command: "run"}, Inherit: inherit}
}

var all = []string{"instructions", "mcp", "skills", "settings", "memory"}

func TestInheritNamesOnlyCategoriesEachOnce(t *testing.T) {
	got, err := Inherits(profile("x", "mcp", "memory"))
	require.NoError(t, err)
	assert.Equal(t, map[Category]bool{MCP: true, Memory: true}, got)

	got, err = Inherits(profile("x"))
	require.NoError(t, err)
	assert.Empty(t, got, "no list inherits nothing")

	_, err = Inherits(profile("x", "plugins"))
	assert.ErrorContains(t, err, `"plugins" is not a category`)
	_, err = Inherits(profile("x", "mcp", "mcp"))
	assert.ErrorContains(t, err, "named twice")
}

func TestABackendWithNoAdapterRunsOnlyAProfileThatInheritsEverything(t *testing.T) {
	tb := Table{}
	err := tb.Check(profile("other", "mcp"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `backend "other" has no adapter`)
	assert.Contains(t, err.Error(), "instructions, skills, settings, memory", "every category it cannot shut out is named")
	assert.NotContains(t, err.Error(), "mcp,", "an inherited category is not named")
	_, err = tb.Command(profile("other", "mcp"), Launch{})
	assert.Error(t, err, "it is not started either")

	require.NoError(t, tb.Check(profile("other", all...)))
	s, err := tb.Command(profile("other", all...), Launch{})
	require.NoError(t, err)
	assert.Equal(t, Start{Command: "run"}, s, "its own command, unchanged, and nothing added to its environment")
}

func TestAnAdapterThatCannotShutOutACategoryRefusesAProfileThatDoesNotInheritIt(t *testing.T) {
	tb := Table{"p": partial{can: []Category{Instructions, MCP, Skills, Settings}}}
	err := tb.Check(profile("p"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the "p" backend adapter cannot shut out memory`)
	_, err = tb.Command(profile("p"), Launch{})
	assert.Error(t, err)

	require.NoError(t, tb.Check(profile("p", "memory")))
	s, err := tb.Command(profile("p", "memory"), Launch{})
	require.NoError(t, err)
	assert.Equal(t, Start{Command: "run --isolated", Env: []string{"ISOLATED=1"}}, s, "the adapter's command and environment")
}

func TestAProfileThatSetsAnOptionItsAdapterOwnsIsRefused(t *testing.T) {
	tb := Table{"p": partial{can: Categories}}
	p := profile("p", all...)
	p.Startup.Command = "run --owned x"
	err := tb.Check(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `sets --owned, which the "p" backend adapter sets itself`)
	assert.Error(t, tb.Check(p), "inheriting everything does not make it the profile's")
	_, err = tb.Command(p, Launch{})
	assert.Error(t, err)
}

type egresser struct {
	partial
	hosts []string
}

func (e egresser) PassHosts(core.Profile) []string { return e.hosts }

func TestABackendsOwnHostsArePassedBeforeAProfilesEachOnce(t *testing.T) {
	tbl := Table{"own": egresser{hosts: []string{"api.model.example"}}, "plain": partial{}}
	p := profile("own")
	p.Egress.Pass = []string{"registry.acme.example", "api.model.example"}
	assert.Equal(t, []string{"api.model.example", "registry.acme.example"}, tbl.Pass(p))
	p.Backend = "plain"
	assert.Equal(t, []string{"registry.acme.example", "api.model.example"}, tbl.Pass(p), "a backend with no hosts of its own adds none")
	p.Egress.Pass = nil
	assert.Empty(t, tbl.Pass(p))
}
