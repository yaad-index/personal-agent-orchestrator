package meshpb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type method struct {
	name                        string
	clientStreaming, serverStrm bool
}

// services is the contract ADR-0017 and the design name: change a call here only
// together with the design and a new minor version.
var services = map[string][]method{
	"Node": {
		{"Launch", false, false}, {"Kill", false, false}, {"Restart", false, false},
		{"Spawn", false, false}, {"KillTemp", false, false}, {"Deliver", false, false},
		{"SetStream", false, false}, {"View", false, false}, {"Decide", false, false},
		{"QueryPosted", false, false}, {"PushConfig", false, false},
		{"ListHarvests", false, false}, {"GetHarvest", false, false}, {"ReadHarvestFile", false, false}, {"DeleteHarvest", false, false},
	},
	"Master": {{"Session", true, true}},
	"Peer": {
		{"Hello", false, false}, {"Liveness", true, true}, {"MasterLost", false, false},
		{"Probe", false, false}, {"NewMaster", false, false},
	},
}

func TestTheWireHasExactlyTheServicesAndCallsTheDesignNames(t *testing.T) {
	fd := File_pao_mesh_v1_mesh_proto
	assert.Equal(t, protoreflect.FullName("pao.mesh.v1"), fd.Package())
	require.Equal(t, len(services), fd.Services().Len())
	for i := 0; i < fd.Services().Len(); i++ {
		sd := fd.Services().Get(i)
		want, ok := services[string(sd.Name())]
		require.True(t, ok, "unexpected service %s", sd.Name())
		require.Equal(t, len(want), sd.Methods().Len(), sd.Name())
		for j, w := range want {
			md := sd.Methods().Get(j)
			assert.Equal(t, w.name, string(md.Name()), "%s method %d", sd.Name(), j)
			assert.Equal(t, w.clientStreaming, md.IsStreamingClient(), "%s.%s", sd.Name(), md.Name())
			assert.Equal(t, w.serverStrm, md.IsStreamingServer(), "%s.%s", sd.Name(), md.Name())
		}
	}
}

func TestEveryStateChangingCommandCarriesARequestID(t *testing.T) {
	for _, m := range []proto.Message{
		&AgentRef{}, &SpawnRequest{}, &DeliverRequest{}, &StreamRequest{}, &ViewRequest{}, &DecideRequest{},
	} {
		f := m.ProtoReflect().Descriptor().Fields().ByName("request_id")
		require.NotNil(t, f, "%s", m.ProtoReflect().Descriptor().Name())
		assert.Equal(t, protoreflect.StringKind, f.Kind())
		assert.Equal(t, protoreflect.FieldNumber(1), f.Number(), "request_id is field 1 everywhere, so a generic interceptor can read it")
	}
}

func TestAReportCarriesAgentsWithTheirStreamBindingsAndRoundTrips(t *testing.T) {
	in := &Report{Seq: 3, Body: &Report_Agents{Agents: &AgentsReport{Full: true, Agents: []*AgentInfo{{
		Name: "a", Tags: []string{"t"}, State: AgentState_AGENT_STATE_RUNNING, Temporary: true, Recipe: "r", Flag: "idle",
		Bindings: []*StreamBinding{{Channel: "local", Level: 2, Viewer: "v"}},
	}}}}}
	b, err := proto.Marshal(in)
	require.NoError(t, err)
	var out Report
	require.NoError(t, proto.Unmarshal(b, &out))
	assert.True(t, proto.Equal(in, &out))
	got := out.GetAgents().GetAgents()[0]
	assert.Equal(t, "local", got.GetBindings()[0].GetChannel(), "a master chosen by promotion can see the live streams")
}
