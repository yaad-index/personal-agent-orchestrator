package directory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

func agent(name string, tags ...string) *pb.AgentInfo {
	return &pb.AgentInfo{Name: name, Tags: tags}
}

func names(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Node+"/"+e.Agent.GetName())
	}
	return out
}

func TestAReportMakesItsAgentsAddressable(t *testing.T) {
	d := New(nil)
	d.Replace("n2", []*pb.AgentInfo{agent("a", "team"), agent("b", "team", "ops")})
	d.Replace("n3", []*pb.AgentInfo{agent("c", "ops")})

	e, ok := d.Lookup("b")
	require.True(t, ok)
	assert.Equal(t, "n2", e.Node)
	assert.Equal(t, []string{"n2/a", "n2/b", "n3/c"}, names(d.All()))
	assert.Equal(t, []string{"n2/a", "n2/b"}, names(d.ByTag("team")))
	assert.Equal(t, []string{"n2/b", "n3/c"}, names(d.ByTag("ops")), "a tag fans out across nodes")
	assert.Empty(t, d.ByTag("none"))
	_, ok = d.Lookup("nope")
	assert.False(t, ok)
	assert.True(t, d.Has("a"))
	assert.Equal(t, 2, d.Count("n2"))
	assert.Equal(t, 1, d.Count("n3"))
}

func TestAFullReportReplacesWhatTheNodeHadReported(t *testing.T) {
	d := New(nil)
	d.Replace("n2", []*pb.AgentInfo{agent("a"), agent("b")})
	d.Replace("n2", []*pb.AgentInfo{agent("b"), agent("c")})
	assert.Equal(t, []string{"n2/b", "n2/c"}, names(d.All()), "a is gone, c is new")
}

func TestUpsertAndRemoveApplyOneChange(t *testing.T) {
	d := New(nil)
	d.Replace("n2", []*pb.AgentInfo{agent("a", "x")})
	d.Upsert("n2", &pb.AgentInfo{Name: "a", Tags: []string{"y"}, Flag: "idle"})
	d.Upsert("n2", agent("b"))
	e, _ := d.Lookup("a")
	assert.Equal(t, []string{"y"}, e.Agent.GetTags(), "a change replaces the agent as known")
	assert.Equal(t, "idle", e.Agent.GetFlag())
	assert.Empty(t, d.ByTag("x"))

	d.Remove("n2", "a")
	d.Remove("n2", "nothing")
	d.Remove("nowhere", "a")
	assert.Equal(t, []string{"n2/b"}, names(d.All()))
	d.Upsert("n2", &pb.AgentInfo{})
	assert.Len(t, d.All(), 1, "an agent with no name is not an agent")
}

func TestDroppingANodeForgetsItsAgents(t *testing.T) {
	d := New(nil)
	d.Replace("n2", []*pb.AgentInfo{agent("a")})
	d.Replace("n3", []*pb.AgentInfo{agent("c")})
	d.Drop("n2")
	assert.Equal(t, []string{"n3/c"}, names(d.All()), "an agent the master cannot hear about is not routed to")
	d.Drop("never-reported")
	d.Replace("n2", []*pb.AgentInfo{agent("a")})
	assert.True(t, d.Has("a"), "and it returns when the node reports in full again")
}

func TestTheFirstNodeToReportANameKeepsItAndTheOtherIsAConflict(t *testing.T) {
	d := New(nil)
	d.Replace("n3", []*pb.AgentInfo{agent("dup", "t3")})
	d.Replace("n2", []*pb.AgentInfo{agent("dup", "t2"), agent("only")})

	e, ok := d.Lookup("dup")
	require.True(t, ok)
	assert.Equal(t, "n3", e.Node, "n3 reported it first")
	assert.ElementsMatch(t, []string{"n3/dup", "n2/only"}, names(d.All()))
	assert.Empty(t, d.ByTag("t2"), "the conflicting agent is not addressable by tag either")
	assert.Equal(t, []Conflict{{Name: "dup", Node: "n2", HeldBy: "n3"}}, d.Conflicts())
	assert.Equal(t, 1, d.Count("n2"), "only the agent that is in the directory counts")
}

func TestAConflictedNameGoesToTheOtherNodeWhenTheHolderLetsGo(t *testing.T) {
	d := New(nil)
	d.Replace("n3", []*pb.AgentInfo{agent("dup")})
	d.Replace("n2", []*pb.AgentInfo{agent("dup")})
	d.Replace("n4", []*pb.AgentInfo{agent("dup")})
	require.Len(t, d.Conflicts(), 2)

	d.Remove("n3", "dup")
	e, ok := d.Lookup("dup")
	require.True(t, ok)
	assert.Equal(t, "n2", e.Node, "the lowest node id among those that reported it, not whichever reported next")
	assert.Equal(t, []Conflict{{Name: "dup", Node: "n4", HeldBy: "n2"}}, d.Conflicts())

	d.Drop("n2")
	e, _ = d.Lookup("dup")
	assert.Equal(t, "n4", e.Node)
	assert.Empty(t, d.Conflicts())
}

func TestAMastersOwnAgentAlwaysHoldsItsName(t *testing.T) {
	local := map[string]bool{"boss": true}
	d := New(func(n string) bool { return local[n] })
	d.Replace("n2", []*pb.AgentInfo{agent("boss"), agent("other")})

	_, ok := d.Lookup("boss")
	assert.False(t, ok, "boss is the master's own: the directory does not answer for it")
	assert.False(t, d.Has("boss"))
	assert.Equal(t, []string{"n2/other"}, names(d.All()))
	assert.Equal(t, []Conflict{{Name: "boss", Node: "n2"}}, d.Conflicts(), "HeldBy empty: the master holds it")
	assert.Equal(t, 1, d.Count("n2"))
}

func TestTheOutcomeDoesNotDependOnTheOrderOfReports(t *testing.T) {
	// Whichever of n2 and n4 reports a name first holds it; but when the holder
	// leaves, the survivor is chosen by node id, so replaying the same history in a
	// different order of arrival ends in the same place.
	for _, order := range [][]string{{"n2", "n4", "n3"}, {"n4", "n3", "n2"}, {"n3", "n2", "n4"}} {
		d := New(nil)
		for _, n := range order {
			d.Replace(n, []*pb.AgentInfo{agent("dup")})
		}
		first, _ := d.Lookup("dup")
		assert.Equal(t, order[0], first.Node)
		d.Drop(first.Node)
		e, ok := d.Lookup("dup")
		require.True(t, ok)
		want := "n2"
		if first.Node == "n2" {
			want = "n3"
		}
		assert.Equal(t, want, e.Node, "order %v", order)
	}
}
