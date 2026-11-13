# ADR 0017: Mesh transport and the cross-node gaps

**Status:** Proposed (2026-11-13)

## Context

ADR-0013 fixed the shape of the mesh (one master, the rest secondary; a transport that carries coordination, not the stream; a secondary that pushes its stream straight to the channel; manual promotion) and left the wire, and several mechanisms, open. Designing the build against it showed five things the single-node pao already does that must cross a node boundary, which ADR-0013's account of what the transport carries does not name. This ADR chooses the wire, names those five, and decides the deferred mechanisms at the level an ADR should, leaving protocol detail to the code. It extends ADR-0013, which stays in force. The detailed design, with the message definitions and the build order, is `docs/design/mesh-transport.md`.

## Decision

1. **Nodes talk directly, over gRPC on mutually authenticated TLS.** There is no broker. Every connection between nodes is direct, encrypted and authenticated on both sides, and the transport is gRPC with typed messages and streaming. This settles the wire ADR-0013 point 5 deferred, and keeps the alert path of ADR-0013 point 6 independent of the master, which a broker would not.

2. **A node's identity is a key pinned in the mesh configuration.** Each node has a key; the node list in the mesh configuration carries each node's id, address and the fingerprint of its key. A peer whose key is not on the list is refused before any message is read. There is no certificate authority and no trust on first use. Who is calling is taken from the verified key, never from anything in a message, and only the current master may command a node.

3. **Every call between nodes carries a deadline, and a state-changing call is idempotent.** This includes the calls that detect and announce a lost master and the calls that promote a node, so a peer that is reachable but hung produces an error naming it instead of stalling whoever asked. A repeat of a state-changing call, identified by a request id, returns the first result and does not run twice.

4. **Delivering an inbound message to an agent on another node is coordination (G1).** The human talks only to the master, so a message for an agent on a secondary reaches the master first and is handed to that agent's node, which stores and delivers it there exactly as on one node. This adds to what ADR-0013 point 2 says the transport carries; the message is one line, not the stream.

5. **Gate questions and their answers cross the mesh (G2).** The approval gate (ADR-0004) runs on the node that hosts the agent. A question carries an id qualified by its node, so that the operator's answer, which arrives at the master, reaches the node that asked. The time an unanswered question waits is unchanged and stays local: if the master is lost no answer can arrive and the question is denied, which is the gate's existing fail-closed behaviour.

6. **An agent's name is unique across the whole mesh (G3).** ADR-0003 makes a name address one agent; with several nodes that means one agent in the mesh. The master refuses a second agent that announces a name already taken and reports the conflict; the agent keeps running on its node and is not addressable through the mesh. Routing by name or tag resolves across nodes, and the access bound of ADR-0012 point 4 applies exactly as on one node: an agent the caller may not address is indistinguishable from one that does not exist, whichever node it is on.

7. **The local channel is master-only (G4).** It is bound to loopback on its own node, so a secondary cannot post into it directly. A secondary's stream reaches the human through a channel it can post to with the credentials it holds (ADR-0013 point 4). The stream is not relayed through the master, which would break ADR-0013 point 3. Direct streaming from a secondary therefore needs a channel whose credentials work from any node; until one exists, a secondary's agents are reached through the master's channels only.

8. **An agent's own cross-node operations are out of scope (G5).** An agent's calls to pao (ADR-0016) are made to its own node and act there. An agent that asks to spawn or kill on another node has no path to do so.

9. **The configuration is distributed as names, never as secret values.** One mesh document, the same on every node, holds the node list, the channels, the routing and the settings; the master distributes it as a versioned bundle, and a node keeps the last version that validated. A separate node document, local and never distributed, holds the node's own identity and the secrets its environment supplies. pao does not move a secret from one node to another, and reports which secrets a node's document names that the node cannot supply.

10. **A reply to a message a secondary pushed is resolved on demand.** Message ids are qualified by node. A reply that names a message on a channel is resolved by asking the nodes which of them posted it; the master then hands the reply to that agent's node, which resolves the reference against the messages that agent was given, as the single-node layer does. A reference that matches nothing in the retained window is reported as unresolved and never guessed (ADR-0010 point 3), and a message the agent was not given is the same answer as an absent one. Nothing is sent per message pushed.

11. **A stream binding is local to its node and reported to the master.** A secondary keeps streaming to the channels it was bound to if the master is lost. Each node reports its agents and their bindings to the master, so a master chosen by promotion learns which streams are live and can clear or re-level them. A binding it did not set is reported to the operator and left running.

12. **Masters are ordered by an epoch, and promotion stays manual.** The epoch increases each time a node is promoted. Masters are ordered by the pair of epoch and node id: the higher epoch wins, and at equal epochs the lower node id stays master; the other yields on contact and rejoins as a secondary without any action. This is not an election, and nothing chooses a master. A node is promoted by the operator, and the promotion refuses while any reachable peer reports a live master at an equal or higher epoch, unless the operator forces it.

13. **The loss of the master is announced without the master.** Nodes detect it among themselves. One secondary announces it, chosen by a fixed order with suppression by the others, and a duplicate announcement is acceptable where none is not. The announcement goes through the channels the announcing node holds credentials for, or through an operator-supplied alert command, and never through the master.

## What this ADR deliberately does NOT decide

The message and call definitions, the detection and announcement intervals, and the build order, which are in the design document and settle in the code. How two masters, each acting on one human input after a forced promotion, are kept from running the same side-effecting command twice: whether the old master's inbound side must be confirmed stopped before a forced promotion, or the channel adapters deduplicate on the platform's own message id, is left open, and a forced promotion is the only way to reach it. Credential rotation beyond the check in point 9. Whether the local channel may ever accept a relayed stream from a secondary as an explicit, limited exception. How an agent's cross-node operations would be authorized if they were allowed. Real chat-platform channel adapters, which direct streaming from a secondary depends on.

## Consequences

A master can run and talk to agents on secondaries, with every call authenticated by a pinned key, bounded by a deadline and safe to retry, and without a broker that could be the one thing that fails when the master does. The five behaviours that crossed a node boundary without being named are named and decided, so the build does not discover them. Keeping secrets out of what is distributed means no node's key can yield every credential and the master is not a secret store, at the price of provisioning each node's secrets by hand. Ordering masters by epoch and node id makes a heal after a partition deterministic without an election, while leaving promotion, and its one residual risk, in the operator's hands. gRPC and its code generation become a dependency of the build, which the operator accepted for typed contracts and streaming.
