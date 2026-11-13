# ADR 0003: Routing

**Status:** Accepted (2026-11-12)

## Context

ADR-0001 fixed that pao is an input router and that every capability, including a judge, is a routable agent. This ADR fixes the routing model: how an input is addressed to an agent, and how a routed input resolves to a running or a new agent. The selection algorithm for input that names no target is deliberately left open.

## Decision

1. **Routing is the organizing principle.** Every input, from any source, is directed to an agent. There is no special fixed infrastructure for capabilities: worker agents, a judge, and temporary agents are all ordinary routing targets rather than hardwired components. The judge in particular is reached by routing text to it for a verdict, not by a wired-in gate. (The single user-facing coordinator role from ADR-0001 and how it maps onto nodes is left to the transport and failover ADR.)

2. **Routing keys are the agent name and its tags.** Each agent carries a **name** and a set of **tags** in its profile (ADR-0001, the agent profile). An input addresses a target by **name** (one specific agent) or by **tag** (a capability or role). Addressing by tag **fans out** to every agent that carries the tag; that fan-out is the baseline, and how to narrow a tag match to a single target when only one is wanted is part of the deferred selection algorithm below.

3. **A routed input resolves to a running agent or a new one.** Routing decides whether a ready, already-running agent receives the input or pao launches a fresh agent to take it. Launching is an outcome of routing, not a separate spawn-on-event subsystem.

4. **The agent is addressed, never the channel.** Agents are source-agnostic (ADR-0001): routing happens in the orchestrator, which maps a source or channel to a target and hands the agent only a normalized, sender-tagged message. An agent never sees how it was reached.

## What this ADR deliberately does NOT decide

The **destination-selection algorithm** for input that names no target. Explicit addressing by name or tag is the baseline; optional intent-based routing, and a judge agent as the fuzzy-case fallback, are approved in direction but their mechanism is deferred to a later ADR, to be fixed when implementation is close. Also deferred: priority and conflict resolution when several targets match, and the transport that carries a routed message between nodes (its own ADR).

## Consequences

The agents become a flat set of addressable targets behind one front, reached by name or by capability. A new capability joins by registering a name and tags, with no change to the router. The one thing still open, how an unaddressed input chooses its target, is isolated in a later ADR and does not block building the addressed-routing path first.
