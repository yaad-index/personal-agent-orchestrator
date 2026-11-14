# ADR 0018: On-demand remote output (Node.Watch)

**Status:** Proposed (2026-11-14)

Extends ADR-0017.

## Context

ADR-0017 point 7 has a secondary post its agent's output directly to a channel it can reach with its own credentials, and records that this needs a channel whose credentials work from any node, so that until one exists a secondary's agents are reached through the master's channels only.

The only channel that exists today is the local channel, which ADR-0017 point 7 establishes is master-only and bound to loopback on its own node. A secondary posting its agent's stream to its own local channel is therefore never seen by the human at the master, so direct posting alone cannot show a remote agent's output until a shared channel, reachable from several nodes, exists.

The build needs the operator at the master to see a remote agent's output before that shared channel lands.

## Decision

Add **Node.Watch**, a server-streaming call on the Node service, as an on-demand complement to the direct posting of ADR-0017 point 7.

1. The master calls `Node.Watch(agent, level)` on the node that holds the agent. The node streams that agent's rendered entries at the requested level (ADR-0008), the recent ones first and then as they arrive. The master shows them in its own local channel and the pao client follows them, so the operator sees a remote agent's output through the surface they already use.

2. Watch does not replace direct posting. `Node.SetStream`, `Node.View` (ADR-0017 point 7) and the stream binding carried in reports (ADR-0017 point 11) stay the path for when a shared channel exists; Watch is the path that works while the only channel is per-node and master-only.

3. Watch adds no trust. The Node service is already callable only by the node the callee believes is master, at the epoch it believes in (ADR-0017 authorization), so Watch is reachable only by the master, like every other Node call. It is a read, so the idempotency of a state-changing call does not apply.

4. Watch is long-lived, so its lifetime stands in for the short deadline of ADR-0017 point 3. A control call carries a deadline so a hung peer is not waited on; a watch is meant to run for as long as the operator views, so instead it runs until the operator stops it, the connection drops, or the serving node's believed epoch changes, and the transport's keepalive detects a hung peer in its place. Ending on an epoch change follows ADR-0017's authorization: once the epoch moves the caller is no longer the master at that epoch, so the stream ends and the new master re-establishes any watch.

5. Watch is not the relay ADR-0017 point 7 forbids, and it answers that point's open question of whether the local channel may ever accept a relayed stream. Point 7 rules out a secondary's stream being relayed through the master as a posting path onto a channel, which would make the master a dependency of the agent's streaming (ADR-0013 point 3). Watch is instead the operator's own node pulling rendered entries on demand and showing them as the terminal viewer; nothing is forwarded onward, and the agent's output and its direct-posting binding (point 7) do not depend on anyone watching. So the local channel shows output the master pulled for its own operator, never a relayed secondary stream, and point 7's prohibition stands.

6. Watch honours the access bound, so it cannot become an oracle. ADR-0017 point 6 restates ADR-0012 point 4 for mesh routing: an agent the caller may not address is indistinguishable from one that does not exist. Watch reaches an agent's rendered output the same way routing reaches the agent, so the same rule binds it, and a caller not entitled to an agent gets the same answer from Watch as for an absent one. Only the operator can call Watch today, but this is the contract a later, less-trusted caller is bound by.

7. Watch is a separate, always-live view, independent of the pause of the bound-channel rendering (`Node.View`, ADR-0017 point 7). A pause holds back what is pushed to the agent's own channel; it does not hold back a Watch, because Watch is the operator choosing to look now. The two are different views of the same agent and do not gate each other.

## What this ADR deliberately does NOT decide

The shared, multi-node channel that would let direct posting (ADR-0017 point 7) reach the human stays future work. Cross-node replies (ADR-0017 point 10) are unchanged and out of scope here: Watch renders output for viewing and does not carry a reply back. A watched entry is shown for reading and is not itself a reply target; replying to a remote agent's message goes through the message path keyed by its node-qualified id (ADR-0017 point 10), independent of whether the message was seen through Watch. Making a watched line a reply target is deliberately not decided here. Two details are left to the design document: the bound on how many concurrent watches a node serves, and what "the recent ones first" resolves to in practice (how many entries, and from where in the transcript).

## Consequences

The operator at the master can watch a remote agent's output on demand, over an authenticated, master-only stream that ends when the operator stops it or the epoch changes, without waiting for a shared channel and without relaying a secondary's stream through the master as a posting path, which ADR-0017 point 7 rules out. The cost is that viewing is a pull the operator initiates rather than output arriving unbidden, which a shared channel will later provide; the two coexist, and direct posting remains the design for the shared-channel case.
