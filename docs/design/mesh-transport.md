# Mesh transport and failover: design proposal

Status: proposal. It is input to the ADRs that settle what ADR-0013 left open;
it decides nothing by itself. It recommends one answer to each question the ADR
defers, says where the ADR as written does not cover something the design needs,
and proposes the order to build in.

Decided by the operator since the first draft: nodes talk **directly** (section 3),
and the wire is **gRPC with typed messages and streaming** over the mutually
authenticated TLS this proposal describes (section 6). The first draft recommended
a framed JSON stream with no new dependency; the typed contracts and native streaming
of gRPC were judged worth its dependency and code generation, and sections 5, 6, 15
and 16 are revised to match.

## 1. What is fixed

ADR-0013 fixes six things. This proposal holds to all of them.

1. One master, the rest secondary. The human talks only to the master. A
   secondary may post outbound to a channel; the human never addresses one.
2. The transport carries coordination, not the stream: the lifecycle commands of
   ADR-0007 crossing a node boundary, "stream agent X to channel Y at level Z",
   and the view controls (pause, resume, backtrack).
3. A secondary pushes an agent's stream directly to the target channel, never
   relaying it through the master or the transport.
4. Every node holds the orchestrator config (sources, channels, routing,
   credentials). Per-agent data (profiles, transcripts, message store) stays
   local to each node.
5. The transport is pao's own and encrypted. The wire choice is deferred.
6. Failover is detected and announced automatically, by an alert path that does
   not depend on the master. Promotion is manual. There is no election.

## 2. Where the ADR as written does not cover what the design needs

These are not objections to ADR-0013. Each is a thing the single-node code
already does, that has to cross a node boundary, and that the ADR's list of what
the transport carries does not name. They need an ADR (or a clarification that
supersedes the relevant point) before the build reaches them.

**G1. Delivering an inbound message to an agent on another node.** The human
talks only to the master, so a message for an agent that lives on a secondary
reaches the master first and has to be handed on. It is small (one line of text,
an optional reply reference), not the stream, and it is a master directing a
node; but it is not in the ADR's list. The design treats it as coordination.

**G2. Gate questions and their answers.** The approval gate runs on the node
that hosts the agent (it answers a hook over a local socket). A question for the
operator is raised on that node and may be posted straight to a channel, like
any other outbound message. The answer, `/allow q3`, arrives at the master,
because the human talks only to the master, and the question id is local to the
node. So the answer has to be routed back to the node that asked, which needs
node-qualified question ids and one more coordination message.

**G3. Agent names must be unique across the mesh.** ADR-0003 says a name
addresses exactly one agent. With several nodes that has to mean one agent in the
whole mesh, or routing by name is ambiguous. The design makes the master refuse a
second agent announcing a name already taken, and report it.

**G4. The only built channel cannot be a direct-push target.** The local HTTP and
event-stream channel is bound to loopback on its own node by design. A secondary
cannot post into it directly. Delivering a secondary's stream to it would mean
relaying through the master, which point 3 forbids, or binding it beyond
loopback, which the channel refuses. So direct push is real only for a channel
whose credentials work from any node: a chat-platform adapter, which does not
exist yet. Section 12 proposes what to do until one does.

**G5. Each node has its own MCP endpoint and agent tokens.** An agent's token
identifies it to its own node only. An agent on a secondary that asks to spawn or
kill on another node has no path to do it; it would have to go through the master.
The design leaves cross-node agent-initiated operations out of the first build.

## 3. The fork: how nodes talk

There are two shapes the ADR leaves open.

- **Broker.** Every node connects to a relay that moves messages between them
  (a publish and subscribe service, or a pao component that plays that role).
- **Direct.** Nodes connect to each other over an encrypted connection and no
  node sits between two others.

### Recommendation: direct, over mutually authenticated TLS

Direct node to node, over TLS 1.3 with both sides authenticated by a per-node key
pinned in the mesh configuration. Reasons, in order of weight:

1. **Failover independence.** Requirement 6 says the alert path must not depend on
   the master. A broker is a node every other node depends on. If it is the master,
   it dies with the master, and the thing that has to announce the death is gone
   with it. If it is a separate service, the mesh gains a single point of failure
   and a service to run and keep available, which a personal mesh should not have
   to carry.
2. **No broker to run or depend on.** Connection security and key handling are in
   the Go standard library, and the message layer is gRPC (section 6). A broker
   product would have been a second dependency or a new process to deploy on top of
   that, and pao's "own transport" (point 5) rules out reusing one.
3. **The topology is already a hub.** Commands flow from the master to each
   secondary and results flow back. That is a star, not a many-to-many bus. Direct
   connections to one node are simpler than a bus.
4. **A smaller security surface.** The trust relationship is a short list of node
   keys in a file. A broker adds a second place that holds credentials and decides
   who may speak.

What it costs, honestly: every node must be reachable by address by the nodes
that dial it, so a deployment has to supply that (a local network, or an overlay
network). pao does not provide NAT traversal. Every node dials and is dialed: the
master calls each secondary to command it, each secondary calls the master to report,
and the liveness plane connects every pair (section 5).

Direct was the operator's choice. The rest of the design does not depend on it: the
directory, the promotion and rejoin rules and the build order would hold over a broker.

## 4. Identity and encryption

- Each node has an Ed25519 key pair, created by `pao mesh keygen` and kept in the
  node's state directory with owner-only permissions. The private key never leaves
  the node.
- The mesh configuration lists every node: an id, a dialable address and the
  fingerprint (SHA-256 of the public key) of its key. That list is the trust
  anchor. There is no certificate authority and no trust on first use.
- A connection is TLS 1.3 with client certificates required, carried as gRPC
  transport credentials. Each side presents a
  self-signed certificate carrying its public key, and checks the other's
  fingerprint against the list, and checks that the node id in the certificate
  matches the list entry it dialed or accepted. A key not in the list is refused
  before any message is read.
- Key rotation is an edit to the list: add the new fingerprint, distribute the
  configuration, switch the node's key, remove the old fingerprint. For a window
  both fingerprints are accepted for that id.
- Joining a node is an operator act: generate its key, add its entry to the
  configuration on the master, distribute. A node that is not in the list cannot
  connect, so a leaked address is not an opening.

## 5. Topology: two planes

- **Command plane, a star.** The master calls each secondary to command it, as
  ordinary remote procedure calls. Each secondary keeps one long-lived stream open to
  the master, over which it reports what it hosts and what changes.
- **Liveness plane, a full mesh.** Every pair of nodes keeps a lightweight stream used
  only for heartbeats, and the peers also answer the failover calls of sections 9 and
  10. It exists because secondaries have to tell whether the master is lost without
  asking the master, and have to coordinate who announces it. For the handful of nodes
  of a personal mesh a full mesh is cheap. The node with the lower id dials the higher,
  to avoid two connections per pair.

A node that loses a connection redials with backoff. Nothing about a lost command
connection stops the agents on that node or their direct streams. gRPC's own keepalive
detects a dead connection quickly; the heartbeats carry what keepalive cannot, the
sender's role and epoch.

## 6. Wire and protocol

The wire is gRPC over the mutually authenticated TLS of section 4, with messages
defined in protocol buffers. The definitions live in the repository and the generated
Go is committed (build step N1 sets that up).

**Three services, split by who serves them.**

```proto
syntax = "proto3";
package pao.mesh.v1;

// Served by every node. The master calls it on a secondary.
service Node {
  rpc Launch(AgentRef) returns (Result);            // ADR-0007
  rpc Kill(AgentRef) returns (Result);
  rpc Restart(AgentRef) returns (Result);
  rpc Spawn(SpawnRequest) returns (SpawnResult);    // ADR-0006
  rpc KillTemp(AgentRef) returns (KillResult);
  rpc Deliver(DeliverRequest) returns (DeliverResult);   // inbound message (G1)
  rpc SetStream(StreamRequest) returns (Result);    // bind or clear a stream
  rpc View(ViewRequest) returns (Result);           // pause, resume, backtrack (ADR-0008)
  rpc Decide(DecideRequest) returns (Result);       // a gate answer (G2)
  rpc QueryPosted(PostedQuery) returns (PostedReply);    // section 11
  rpc PushConfig(ConfigBundle) returns (ConfigAck); // section 7
}

// Served by the node that is master. A secondary calls it.
service Master {
  // Opened by a secondary and kept open. Up: the node's agents with their stream
  // bindings (full, then deltas) and state events. Down: acknowledgements.
  rpc Session(stream Report) returns (stream Ack);
}

// Served by every node. Any node calls it on any other.
service Peer {
  rpc Hello(HelloRequest) returns (HelloReply);          // id, epoch, role, versions
  rpc Liveness(stream Beat) returns (stream Beat);       // heartbeats
  rpc MasterLost(MasterLostNotice) returns (Ack);        // an announcement (section 10)
  rpc Probe(ProbeRequest) returns (ProbeReply);          // promotion step 1 (section 9)
  rpc NewMaster(NewMasterNotice) returns (Ack);          // promotion step 3
}
```

**Why commands are calls and not frames on a stream.** A long-lived stream that carries
commands would give up two of the reasons for choosing gRPC: a deadline per command and
a typed result per command. As calls, each command has a native gRPC deadline, a typed
response and a status code, and the generated client and server stubs enforce the
contract. Streams are used where they fit: the ordered upward feed of reports, and the
heartbeats. The cost is that the master must be able to dial every secondary, which the
liveness plane already requires of every node. If a secondary could not be dialed, the
command plane could run over its `Session` stream instead, at the price of per-command
deadlines; that variant is not designed or built here.

**Identity and authorization.** The caller's node id comes from the verified
certificate, never from anything in the message. An interceptor on each service
checks it: only the current master may call `Node`; any node in the list may call
`Master.Session` and `Peer`. A secondary cannot command anyone.

**Epoch.** Every call carries the caller's master epoch in call metadata. An interceptor
refuses a command whose epoch is lower than the highest the receiver knows, with
`FAILED_PRECONDITION`.

**Idempotency.** State-changing calls carry a `request_id`. The receiver keeps a bounded,
time-limited cache of request id to result and answers a repeat with the first result,
so a retry after a lost response does not run a command twice. This is application
level; gRPC does not provide it.

**Errors.** gRPC status codes carry the outcome: `NOT_FOUND` for an unknown agent,
`ALREADY_EXISTS` for a name conflict, `PERMISSION_DENIED` for a caller that is not the
master, `FAILED_PRECONDITION` for a stale epoch, `DEADLINE_EXCEEDED` and `UNAVAILABLE`
for time and reachability. A denied or missing agent yields the same code and text, as
ADR-0012 requires.

**Versioning.** The package is `pao.mesh.v1`; changes within it are additive. `Hello`
exchanges the versions each side speaks, and a node refuses a peer it has no common
version with and says so.

**What the transport does not carry.** Agent output, transcripts, harvest contents,
attachment bytes. A call that names a harvest or a file by reference resolves it on the
node that holds it.

## 7. The configuration, and credentials

ADR-0013 point 4 says every node holds the orchestrator config. The question is
how it is kept the same, and how credentials get to each node and stay current.

**Recommendation.** Split the document in two.

- The **mesh document** is the same on every node: the node list with addresses and
  fingerprints, the channels and their settings, the routing, the gate and
  temporary-agent settings. It never contains a secret value. Where it needs one it
  names where to find it, as the channel token already does (`token_env` names an
  environment variable; the secret is not in the file).
- The **node document** is local and never distributed: the node's own id, listen
  address, state directory, terminal socket, key location, and the actual secrets
  its environment supplies.

The master distributes the mesh document over the command plane as a versioned
bundle (`Node.PushConfig`, with a monotonic version and a hash). A node applies it
only if it validates, answers with the version it runs (`ConfigAck`), and keeps the
last good one. A node that restarts takes the master's current version on connect.
The configuration a node was started with is its starting point until the master
sends a newer one, so a master that is down does not strand it.

**Credentials.** Because the document holds names, not values, pao does not move
secrets between nodes. The operator provisions each node's environment (or secret
files) with the secrets the document names. This keeps the master from being a
secret store, and a compromise of one node's transport key does not yield every
credential.

Keeping credentials current is then an operational act, supported by one command:
`pao mesh check` reports, for every node, which secrets the current document names
that the node cannot see, without revealing any value. A rotation is: change the
secret on each node, run `pao mesh check`, then change the document if a name
changed. If a later release wants pao to distribute secrets, it would be an
explicit, separately decided feature with its own threat model; this proposal does
not assume it.

## 8. The agent directory, routing and streaming

**Directory.** The master keeps a directory of every agent in the mesh: name, tags,
node, state, flags, stream bindings. Each node sends it, as `Report` messages on its
`Master.Session` stream: its agents in full when the stream opens, then each change as
it happens. The directory is rebuilt from those reports, never persisted, so it cannot
go stale across a restart or a promotion.

**Names (G3).** A name must be unique across the mesh. The master refuses an
report that reuses a name and tells the operator of the conflict. The node
keeps the agent running locally; it is simply not addressable through the master.

**Routing.** Routing by name or tag resolves against the directory, so a tag fans
out across nodes. The access filter of ADR-0012 point 4 applies exactly as on one
node: a caller that may not address an agent gets the same answer as for a missing
one, whichever node it is on. For a remote agent, delivery is `Node.Deliver` to its
node, which stores the message in its own message store and delivers it by the
existing local path. The reply to the caller is the per-agent outcome the local
path already reports.

**Streaming (point 3).** To show the human an agent's output, the master sends
`Node.SetStream {agent, channel, level, viewer}` on the agent's node. That node binds the
agent to the channel (the binding the communication layer already has) and its
transcript follower posts there directly, rendered at the requested level, using
the credentials it holds. `SetStream` with no channel clears it. A secondary keeps
streaming to the channels it was told to if the master is lost: the binding is
local state. Because it is local, the master does not know it unless told, so every
binding is carried in the node's `Report` messages on `Session`, in the full report
when the stream opens and in a delta when it changes. That is how a master rebuilt by promotion (section 9) learns which streams
are live, and can clear or re-level one that it did not itself set.

**View controls.** `Node.View` carries the viewer's pause, resume and backtrack to
the node that renders for that viewer. As ADR-0008 says, these act on the rendered
view only. Pausing the agent itself has no owning ADR and is not carried.

## 9. Master epochs, promotion and rejoin

**Epoch.** A number that increases each time a node is promoted. Every message
carries the sender's belief. A node persists the highest epoch it has seen and the
id of that epoch's master. A command from a lower epoch is refused. This is not an
election: nothing here chooses a master. It only lets a node tell a current master
from a stale one.

**Equal epochs.** Two nodes on opposite sides of a clean partition each see no peer
that reports a master, so neither refuses, and both can promote to the same epoch.
The order is therefore on the pair (epoch, node id), not the epoch alone. At equal
epochs the master whose node id sorts lower stays master and the other yields on
contact. The rule is arbitrary, and that is the point: any fixed total order makes a
heal deterministic, and this one is easy to state and to test. The yielding node
takes the same path as a lower-epoch master in the rejoin rules below.

**Promotion.** `pao promote` is run by the operator on the node to become master.

1. The node calls `Peer.Probe` on every reachable peer, which answers with its epoch
   and whether it has a live command connection to a master.
2. If any peer reports a live master at an epoch at least as high as the highest
   known, the node refuses and says which, unless the operator passes `--force`.
   This is the guard against promoting a node while the old master is merely
   unreachable from one place.
3. Otherwise it sets its epoch to the highest seen plus one, persists it, starts
   receiving from the human channels, and calls `Peer.NewMaster` with the new epoch on every
   peer.
4. Each peer closes its command connection to the old master, dials the new one, and
   reports its agents on a new `Session`. The new master's directory is complete as
   the reports arrive, and because each report carries the node's active stream
   bindings (section 8), the new master also knows which streams are live and
   can clear or re-level them. It reconciles them against what it holds nothing for:
   a binding it did not set is reported to the operator and left running until told
   otherwise, not silently dropped.

Promotion changes the role, not the data: per-agent data stays where it is. The
agents that were local to the old master are not available through the new one
while the old master is down, and come back when it rejoins.

**Rejoin.** A node that starts, or reconnects, with a lower (epoch, node id) than the mesh
holds becomes a secondary on contact, without anyone's action. It releases the
human channels' inbound side (stops receiving from them), dials the current master,
reports its agents, and takes the current configuration. This is deterministic
from the epochs and needs no vote.

**What is not prevented.** If the old master is alive but partitioned away when a
node is promoted, both can act as master until the partition heals. The guard in
step 2 makes this an operator decision with the facts in front of them, and the
`MasterLost` notice tells them what the secondaries saw. When the partition
heals the master with the lower epoch yields, and at equal epochs the one with the
higher node id does (the rule above). Receiving from a human channel twice during the
window is possible. For a message that is delivered twice, that is a duplicate. For
a command with a side effect it is not: two masters that each read the same human
input independently generate their own commands, so a spawn could happen twice, and
the request-id idempotency of section 6 does not help, because it covers one master's
retries and not two masters' separate commands. Whether a channel can be read by
two masters at once at all depends on the channel (a poll against one account can,
a single delivery address usually cannot), so the exposure is channel-specific. It is
an open question for N0 whether a forced promotion should require the old master's
inbound side to be confirmed stopped, or whether the channel adapters should
deduplicate on the platform's own message id. Removing the window fully would need an
election or an external lock, which ADR-0013 rejected on purpose.

## 10. Detecting that the master is lost, and announcing it

**Detection.** Each node sends a heartbeat to every peer every 2 seconds. A node
declares the master lost when it has seen no heartbeat from it on the liveness
plane and no traffic on the command plane for the dead interval (default 15
seconds), and a redial has failed. Both numbers are configuration. A false alarm
costs one extra message; it never promotes anything.

**Announcing, without the master.** Every secondary that detects the loss would
announce it, which would send the human one alert per node. To keep it to one in the
usual case without an election:

- The secondaries are ranked by node id. Rank 0 announces at once; rank n waits
  n times a stagger interval (default 3 seconds).
- Before announcing, a node checks whether it has received a `MasterLost` notice for
  this epoch from a peer. If so it stays silent. After announcing it sends
  a `MasterLost` notice to its peers.
- If the liveness plane is partitioned, two nodes can announce. That is accepted: a
  duplicate alert is better than none, and nothing it triggers is automatic.

**The path.** The announcement does not go through the master, which is gone. The
announcing node posts it to the channels the mesh document lists under its alert
settings, using the credentials it already holds (point 4), and, if configured,
runs an alert command on that node (for example one that calls a push service),
which is a way to reach the operator that does not depend on any chat platform
being up. If neither is configured the alert is logged on every node and shown by
`pao mesh status`, and the mesh document check warns that failover alerting is not
set up. G4 applies here too: until a channel exists that a secondary can post to,
the alert command is the only master-independent path.

## 11. A reply to a message a secondary pushed

ADR-0010 resolves a reference to a message through the layer's own id map, which is
per node. A human replies to a message an agent on a secondary posted. The reply
arrives at the master.

**Recommendation.** Resolve on demand, with no per-message traffic.

- A reply that names a platform message id (the channel's own id) is resolved by
  `Node.QueryPosted {channel, id}` called on the nodes. The node that posted it, from the
  index its channel adapter keeps of what it posted, answers with the agent and its
  own pao id. Only the master asks, and only when a human replies, so the cost is a
  few small messages on a rare event rather than a note for every message pushed.
- The master then sends the reply to that agent's node as an ordinary `Node.Deliver`
  that carries the node-local reference, and the node resolves it in the agent's
  scope exactly as on one node.
- A quoted reply (text, no id) addressed to one agent is forwarded the same way and
  resolved by the agent's node. A quoted reply addressed to several agents is
  refused, as the communication layer already does on one node; ADR-0010 itself
  does not say so, and an ADR that settles the mesh case should.
- Out of window, unknown and unauthorized are the same answer, not found. Nothing is
  guessed.
- Pao ids are made globally unique by prefixing the node id, so an id shown to a
  human or logged anywhere in the mesh names its node.

The posted index is a rolling record held by the posting node, bounded by the same
window as its message store. It is node-local data, as point 4 requires.

## 12. Gate questions and the local channel

**Gate (G2).** The gate keeps running on each node. A question carries a
node-qualified id, `s2.q3`. The node posts it to the channels directly. The
operator's `/allow s2.q3` arrives at the master, which calls `Node.Decide` on node
`s2`. The unanswered-question timeout is unchanged and local: if the master is lost
no answer can arrive and the question is denied when its time runs out, which is the
existing fail-closed behaviour.

**The local channel (G4).** Recommendation: treat it as a master-only channel and
say so. It carries the human's conversation with the master and the streams of
agents that run on the master. A secondary's stream reaches the human through a
channel it can post to. If the operator wants a secondary's output in the local
channel during development, the one workable way is an authenticated ingest on the
master, reachable over the mesh connection, which is a relay through the master and
therefore an exception to point 3. If the owner wants that exception it should be
decided explicitly and limited to the local channel; the design does not assume it.

## 13. What happens when things go wrong

| situation | behaviour |
|---|---|
| A secondary loses the master | Its agents keep running; streams already bound keep posting directly; it redials with backoff; commands from the master's lower epoch are refused if a new master exists. |
| The master is lost | Secondaries detect and announce (section 10). The human cannot reach any agent until a node is promoted. Gate questions time out and deny. |
| Promotion while the old master is alive elsewhere | Refused unless forced (section 9); if forced, both act until the partition heals, then the lower epoch yields (at equal epochs, the higher node id). |
| A node restarts | It takes its persisted epoch and last good configuration, dials the master, reports its agents, and takes the current configuration. |
| A secondary announces a name already taken | The master refuses and reports it; the agent keeps running locally and is not addressable through the mesh. |
| A command's deadline passes | The sender gets `DEADLINE_EXCEEDED`; a retry with the same request id gets the first result back and does not run the command twice. |
| A configuration push fails validation on a node | The node keeps its last good version and answers with the error; the master reports which node is behind. |

## 14. Security notes

This design adds a network listener to every node. It is bound to the address in the
node document and accepts only a peer whose key is in the list. It carries commands
that start and stop agents, so the same care as the operator surface applies.

- The transport authenticates nodes, not agents. An agent still reaches pao only
  through its own node's surfaces and tokens (SECURITY.md).
- Anything that could read a node's private key or its mesh configuration could
  impersonate the node. Agents share the node's operating-system user (the known
  limitation in SECURITY.md), so a node's key file and configuration are readable to
  its agents unless the gate or the operating system stops them. The key file's
  location should be added to the paths a gate rule denies once that rule exists.
- A node adopts a master it hears of from any listed node, in a heartbeat or a hello, if
  that master supersedes the one it holds. That is how a master that rejoins learns to
  yield without being told directly, and it means a listed node can lie about who is
  master. The lie then spreads: an honest node that adopts a false belief sends it on in
  its own heartbeats, so it cascades through the whole liveness mesh and not only to the
  nodes the liar talks to. A compromised member can therefore disrupt master consensus
  mesh-wide, naming any listed node master at a high epoch, and not only impersonate
  itself. The trust model is that the nodes on the list are trusted not to; a node that
  is compromised is outside it, and so is a mesh whose list is edited by someone who
  should not.
- A compromised secondary can send commands only within what the master accepts from
  a secondary (announce, results, events, alerts, query answers), and cannot command
  the master or another secondary. The master, in turn, can command every secondary:
  the master is the most sensitive node.

## 15. Build order

Each step is a separate, reviewable change. N1 adds the one new dependency (section 16); no other step adds one.

- **N0. Settle the gaps (documents).** An ADR (or two) that clarifies or supersedes
  the points of ADR-0013 for G1 to G5: inbound delivery and gate decisions cross the
  mesh, names are mesh-unique, the local channel is master-only, and cross-node
  agent-initiated operations are out of scope. Needs the owner's approval.
- **N1. Node identity, the mesh configuration, and the protocol toolchain.** Node ids,
  `pao mesh keygen`, the mesh section (node list with addresses and fingerprints), the
  split between the mesh and node documents, validation, and gRPC transport credentials
  with pinned fingerprints. The `.proto` definitions, the committed generated Go, a
  `go generate` target with the generator versions pinned, and a check in CI that the
  generated files are current. Tests on the validation and on the credentials in process.
- **N2. Transport core.** The three services with their interceptors (identity from the
  certificate, authorization by role, the epoch check), `Hello`, the `Liveness` stream,
  connection backoff and keepalive settings, and the request-id cache. Tests over real
  mutual TLS on loopback, including a refused unknown key, a refused wrong node id, a
  secondary refused on `Node`, and a stale epoch refused. `pao mesh status` shows the
  peers and their state.
- **N3. The agent directory and remote delivery.** In two parts. First, `Report`
  messages on `Master.Session`, the master's directory (rebuilt from reports, never
  persisted, mesh-unique names with the first holder keeping a name), and the reporter a
  secondary runs. Then routing that resolves a name or tag to a remote agent, and
  `Node.Deliver`, which a message for a remote agent needs and so belongs here rather than
  with the other commands: a remote Deliverer behind the router's existing interface.
- **N4. The command plane.** `Node.Launch`, `Kill`, `Restart`, `Spawn` and `KillTemp`,
  with typed results, native deadlines and idempotency by request id.
  Harvests stay on the node that made them; listing and reading them goes through the
  master as a request to that node.
- **N5. Streams and view controls.** `Node.SetStream` and `Node.View`: binding on the node
  and direct posting, level, and clearing. Needs a channel a secondary can post to;
  tested with a test channel that is reachable from several nodes.
- **N6. Replies across nodes.** Node-prefixed ids, the posted index, `Node.QueryPosted`,
  and the forwarded reply path.
- **N7. Gate across nodes.** Node-qualified question ids and `Node.Decide`.
- **N8. Configuration distribution.** The mesh document push, versions, `ConfigAck`,
  last-good retention, and `pao mesh check` for secrets.
- **N9. Failover detection and alerting.** Liveness plane, master-lost detection, the
  ranked announcer with suppression, the channel path and the alert command, and the
  persisted epoch.
- **N10. Promotion and rejoin.** `pao promote` with its guard, epoch fencing, the
  directory rebuild, automatic demotion on contact with a higher epoch, and a runbook.
- **N11. Multi-process tests and stabilization.** Three nodes as separate processes on
  loopback with the fake agent over real tmux: command routing, stream binding, master
  loss and announcement, promotion, rejoin, partition and heal. The end-to-end tests
  that depend on terminal timing have shown flakiness under load and should get a timing
  fix before more are added.

N1 to N4 are the minimum for a master to run an agent on a secondary and talk to it.
N5 and N6 need a real remote-capable channel to be fully exercised. N9 and N10 are
independent of N5 to N8 and could be built earlier if failover matters more than
streaming.

## 16. Dependencies

One new dependency, chosen by the operator: gRPC and protocol buffers. It is added in
build step N1, not by this proposal.

| module | version | notes |
|---|---|---|
| `google.golang.org/grpc` | v1.85.0 | latest stable at the time of writing; requires Go 1.26.0, which this module already uses |
| `google.golang.org/protobuf` | v1.36.12 | the protocol buffer runtime |
| `golang.org/x/net` | v0.60.0 | pulled in by gRPC |
| `golang.org/x/text` | v0.42.0 | pulled in by gRPC |
| `golang.org/x/sys` | v0.48.0 | pulled in by gRPC; newer than the version the module records |
| `google.golang.org/genproto/googleapis/rpc` | v0.0.0-20260817212433-ac3dfec99bb1 | the status messages gRPC uses |

The versions are what resolving gRPC and protobuf from an empty module selects; the
real list is whatever `go mod tidy` records in N1, and that review is where it is
checked.

**Build tooling, not linked into the binary.** Generating Go from the `.proto` files
needs `protoc` and two plugins: `protoc-gen-go` (from the protobuf module, v1.36.12) and
`protoc-gen-go-grpc` (its own module, v1.6.2). The generated code is committed, so a
normal build and the tests need none of them. A CI step regenerates the code and fails
if the committed files differ, which needs `protoc` in CI at a pinned version with a
verified checksum. That is a second thing to approve, separate from the module
dependency: a build-time tool and its installation in CI.

Considered and not chosen: a message broker or its client library (the fork); a
framed JSON stream over TLS with no new dependency (the first draft's
recommendation, replaced by the operator's decision above); a WebSocket library.

## 17. Questions for the owner

Decided: the fork (direct) and the wire (gRPC over mutual TLS, sections 3 and 6).
Still open, each with this proposal's recommendation, to be settled in the ADR of
build step N0:

1. **The local channel (G4):** master-only (recommended), or an explicit relay
   exception for development?
2. **Credentials:** names in the distributed document and secrets provisioned per
   node (recommended), or pao distributing secret values?
3. **Mesh-unique names (G3):** is "one name, one agent in the whole mesh" the rule?
4. **Agent-initiated cross-node operations (G5):** out of the first build?
5. **Alert path:** is an operator-supplied alert command acceptable as the
   master-independent path until a remote-capable channel exists?
6. **The codegen toolchain:** is `protoc` with pinned plugins, and a CI check that the
   committed generated code is current, acceptable?
7. **Two masters during a forced promotion (section 9):** confirm the old master's
   inbound is stopped before a forced promotion, or have channel adapters deduplicate
   on the platform's message id?
