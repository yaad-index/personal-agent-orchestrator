# ADR 0018: On-demand remote output (Node.Watch)

**Status:** Proposed (2026-11-14)

Extends ADR-0017.

## Context

ADR-0017 point 3 has a node post a remote agent's output directly to the channel it was bound to, using the node's own credentials. ADR-0017 point 7 records that this needs a channel whose credentials work from any node, and that until one exists a secondary's agents are reached through the master's channels only.

The only channel that exists today is the local channel, which ADR-0017 point 7 establishes is master-only and bound to loopback on its own node. A secondary posting its agent's stream to its own local channel is therefore never seen by the human at the master, so direct posting alone cannot show a remote agent's output until a shared channel, reachable from several nodes, exists.

The build needs the operator at the master to see a remote agent's output before that shared channel lands.

## Decision

Add **Node.Watch**, a server-streaming call on the Node service, as an on-demand complement to the direct posting of ADR-0017 point 3.

1. The master calls `Node.Watch(agent, level)` on the node that holds the agent. The node streams that agent's rendered entries at the requested level (ADR-0008), the recent ones first and then as they arrive, until the master cancels or the call's deadline passes. The master shows them in its own local channel and the pao client follows them, so the operator sees a remote agent's output through the surface they already use.

2. Watch does not replace direct posting. `Node.SetStream`, `Node.View` and the stream binding carried in reports (ADR-0017 point 3) stay the path for when a shared channel exists; Watch is the path that works while the only channel is per-node and master-only.

3. Watch adds no trust. The Node service is already callable only by the node the callee believes is master, at the epoch it believes in (ADR-0017 authorization), so Watch is reachable only by the master, like every other Node call. It carries a deadline like every inter-node call (ADR-0017 point 3); it is a read, so the idempotency of a state-changing call does not apply.

## What this ADR deliberately does NOT decide

The shared, multi-node channel that would let direct posting (ADR-0017 point 3) reach the human stays future work. Cross-node replies (ADR-0017 point 10) are unchanged and out of scope here: Watch renders output for viewing and does not carry a reply back.

## Consequences

The operator at the master can watch a remote agent's output on demand, over an authenticated, deadline-bounded, master-only call, without waiting for a shared channel and without relaying a secondary's stream through the master as a posting path, which ADR-0017 point 7 rules out. The cost is that viewing is a pull the operator initiates rather than output arriving unbidden, which a shared channel will later provide; the two coexist, and direct posting remains the design for the shared-channel case.
