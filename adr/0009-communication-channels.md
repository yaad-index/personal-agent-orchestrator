# ADR 0009: Communication channels (human ↔ pao)

**Status:** Accepted (2026-11-13)

## Context

pao drives several agents, across machines, behind one human-facing front (ADR-0001). This ADR fixes the communication layer: what a channel is, how inbound sources reach pao, and how the single human-in-the-loop line is kept unified and reliable.

## Decision

1. **Source, channel, and layer are distinct.** A **source** is any inbound origin (a chat platform, a peer agent, a reminder, a system event). A **channel** is the subset of sources that are a human front, the human↔pao line of ADR-0001; no channel runs between pao and an agent. The **communication layer** is the one component that handles every source. How that traffic moves between nodes is the transport ADR, and the message schema itself is the message-model ADR; this ADR fixes the layer and the source/channel boundary.

2. **One layer handles every source.** pao handles all sources in one layer, so human and peer traffic share one component rather than living in separate stacks.

3. **Inbound is normalized to a source-agnostic, sender-tagged message.** Every source is reduced to `[sender]: text` before anything downstream sees it; the agent never learns which source or transport carried it (ADR-0001). The source kind is orchestrator-side only and never reaches the agent.

4. **Inbound preprocessing lives in the layer.** Source-specific work that must happen before routing, such as turning a voice note into text or expanding a reminder or a system event into a message, happens here, once, so no downstream component and no agent carries a per-source special case.

5. **Outbound to the human is a single conduit.** Everything pao sends the human, both agent output and pao's own messages such as an approval prompt or a control-plane alert, leaves through the layer, so the one human-in-the-loop line is unified. The policy applied on that conduit is a separate ADR (see below).

6. **For agent output, capture is guaranteed by the transcript.** The agent output pao sends the human is taken from what the backend itself wrote (ADR-0008's transcript mirror), so that output cannot be lost before pao sends it. Actual delivery to the human is the platform's, and pao's own non-transcript messages (point 5) are captured by whatever raised them, not by a transcript.

## What this ADR deliberately does NOT decide

The message model (its schema, message ids, quote-as-fallback, fetch-by-id, and reactions versus action buttons) is the next ADR. The routing algorithm, which agent an inbound reaches, stays deferred (ADR-0003 set the direction). The outbound policy itself (quiet hours, rate-limiting, coalescing, the approval gate's exemption from them, and budget-driven shedding) is the budget-and-outbound-notifications ADR. The node transport that moves traffic between nodes, and which node holds a given channel's credentials when agent output is streamed to a channel, are the transport ADR. Which platforms ship first, and each source adapter's implementation, are left open.

## Consequences

The human gets one reliable line to the whole set of agents, and any agent that blocks or asks surfaces in that one place. Handling every source in one layer means a new source is an adapter in a single place, and agents and backends stay source-agnostic. Keeping source, channel, and layer distinct holds the human-only boundary of ADR-0001 intact while still letting peer and system traffic ride the same layer. The outbound conduit gives one choke point for the policy a later ADR defines.
