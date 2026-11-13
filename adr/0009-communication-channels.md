# ADR 0009: Communication channels (human ↔ pao)

**Status:** Proposed (2026-11-13)

## Context

pao drives several agents, across machines, behind one human-facing front (ADR-0001). This ADR fixes the channel layer: what a channel is, how sources reach it, and how the single human-in-the-loop line is kept unified and reliable.

## Decision

1. **One communication layer owns every source.** pao provides a single layer that carries both its inter-node and peer-agent traffic and its human-facing sources, so there is one layer for many sources rather than a separate transport plus a per-platform chat integration.

2. **A channel connects the human to pao, and only the human to pao.** This restates the ADR-0001 boundary as this ADR's subject: there is no channel between pao and an agent, which pao reaches by the terminal-in, transcript-out path of ADR-0001. "Channel" always means a human front.

3. **Inbound is normalized to a source-agnostic, sender-tagged message.** Every source is reduced to `[sender]: text` before anything downstream sees it; the agent never learns which source or transport carried it (ADR-0001). The source kind (a chat platform, a peer agent, a reminder, a system event) is orchestrator-side only and never reaches the agent.

4. **Inbound preprocessing lives in the channel layer.** Source-specific work that must happen before routing, such as turning a voice note into text or expanding a reminder or a system event into a message, happens here, once, so no downstream component and no agent carries a per-source special case.

5. **Outbound to the human is a single conduit.** Everything pao sends the human leaves through the channel layer, so the one human-in-the-loop line is unified and reliable by construction. The policy applied on that conduit is a separate ADR (see below).

6. **The reliability guarantee is the transcript.** Because pao sends the human from what the backend itself wrote (ADR-0008's transcript mirror), "it reached the human" is structural, not dependent on a component emitting the message separately.

## What this ADR deliberately does NOT decide

The message model (message ids, quote-as-fallback, fetch-by-id, and reactions versus action buttons) is the next ADR. The routing algorithm, which agent an inbound reaches, stays deferred (ADR-0003 set the direction). The outbound policy itself (quiet hours, rate-limiting, coalescing, and the approval gate's exemption from them, plus budget-driven shedding) is the budget-and-outbound-notifications ADR. The node transport that carries traffic between nodes, and which node holds a given channel's credentials when agent output is streamed to a channel, are the transport ADR. Which platforms ship first, and each source adapter's implementation, are left open.

## Consequences

The human gets one reliable line to the whole set of agents, and any agent that blocks or asks surfaces in that one place. Carrying inter-node traffic and the human sources in one layer keeps the moving parts down. Because inbound is normalized and preprocessed once, agents and backends stay source-agnostic, so a new source is an adapter in a single place. The outbound conduit gives one choke point for the policy a later ADR defines.
