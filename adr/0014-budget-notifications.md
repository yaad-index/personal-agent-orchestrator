# ADR 0014: Budget and outbound notifications

**Status:** Proposed (2026-11-13)

## Context

pao sends the human messages from many agents and runs many agents against one shared subscription. Left unmanaged, a flapping source floods the human and a burst of agents exhausts the shared window. This ADR fixes the single outbound door and the budget that protects the shared window.

## Decision

1. **One outbound door for proactive notifications (the notifier).** Every message pao sends the human on its own initiative passes through a single notifier that applies quiet hours, rate-limiting, and coalescing, so a flapping source collapses to one line instead of a flood. A stream the human explicitly asked to watch (ADR-0008, ADR-0009) is a requested view, not a notification, and is not held or coalesced by the door. The channel layer of ADR-0009 is the conduit; this is the policy applied on it, in one place.

2. **A closed must-not-wait class bypasses the door.** A short, pao-defined set of messages bypasses the notifier entirely: a gate question (ADR-0004, an agent is blocked on the answer), a failover notice (ADR-0013), and the operator-only pane shown when an agent stalls (ADR-0007). It is a closed list pao controls, not a flag a source can set, so the exemption cannot be used to route around quiet hours. The door's quiet hours, rate-limit, and coalescing apply to everything else pao sends proactively.

3. **Budget is driven by real subscription usage, not token or cost counting.** pao runs on one personal subscription shared across its agents, so the budget signal is the subscription's own utilization (its short-window and weekly usage figures, the same source ADR-0007 reads for usage), not a count of tokens or money.

4. **As the shared window fills, pao sheds low-priority work first.** A degradation ladder: background and low-priority agents are shed or paused before interactive work, and concurrency caps bound how many agents run at once, so the shared window is spent on what the human is actually waiting on.

5. **The gate is the primary control; budget is the backstop.** Under a subscription there is no per-call cost, so the per-action gate (ADR-0004) governs what runs; budget exists only to keep the shared window from being exhausted, not to price individual actions.

## What this ADR deliberately does NOT decide

The notifier's exact quiet-hours, rate-limit, and coalescing parameters. The precise degradation ladder (which agent classes are shed in what order) and the concurrency-cap numbers. How "background" and "low-priority" are marked on an agent. The usage-reading mechanism itself (ADR-0007). How a shed or paused agent is resumed (relates the control plane and the still-open agent-pause question). Where the notifier runs when secondaries post directly to channels (ADR-0013), one logical policy versus a per-node door: on the single-node MVP it is one door, and the multi-node composition is deferred.

## Consequences

The human gets a managed outbound stream instead of a flood, with the must-not-wait class, an approval or a failover notice, always getting through. The shared subscription window is protected by shedding background work before interactive work rather than by metering every call, which fits a personal subscription where the gate already governs what runs. Keeping the notifier and the budget as one door and one signal means a later change to quiet hours or to the ladder is one place, not scattered across sources.
