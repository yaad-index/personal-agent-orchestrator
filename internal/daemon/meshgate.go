package daemon

import (
	"context"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// gateDecider takes the operator's answer to a gate question and gives it to the node
// that asked (ADR-0017, G2). A question carries the id of the node that asked, so on
// the master "s2.q3" goes to node s2 and "q3" or "<this node>.q3" is its own. A node
// that is not the master answers only its own questions.
type gateDecider struct {
	pending *gate.Pending
	// cmds is nil on a node that is not in a mesh.
	cmds *meshCommands
}

// answerTimeout bounds the call that carries an answer to another node.
const answerTimeout = readCallTimeout

func (d *gateDecider) Answer(id string, ans gate.Answer) error {
	self := d.pending.Node()
	node, _, qualified := strings.Cut(id, ".")
	if !qualified && self != "" {
		// A bare id is this node's question, as on a node outside a mesh.
		id, node = self+"."+id, self
	}
	if d.cmds == nil || node == self {
		return d.pending.Answer(id, ans)
	}
	ctx, cancel := context.WithTimeout(context.Background(), answerTimeout)
	defer cancel()
	return d.cmds.Decide(ctx, node, id, ans)
}
