# ADR 0014: Budget and outbound notifications

**Status:** Proposed (2026-11-13)

## Context

pao sends the human messages from many agents, and runs many agents that draw on different backend accounts. Left unmanaged, a flapping source floods the human, and a burst of agents exhausts whichever account they draw on. This ADR fixes the single outbound door and the per-account budget that protects each account.

## Decision

1. **One outbound door for proactive notifications (the notifier).** Every message pao sends the human on its own initiative passes through a single notifier that applies quiet hours, rate-limiting, and coalescing, so a flapping source collapses to one line instead of a flood. A stream the human explicitly asked to watch (ADR-0008, ADR-0009) is a requested view, not a notification, and is not held or coalesced by the door. The channel layer of ADR-0009 is the conduit; this is the policy applied on it, in one place.

2. **A closed must-not-wait class bypasses the door.** A short, pao-defined set of messages bypasses the notifier entirely: a gate question (ADR-0004, an agent is blocked on the answer), a failover notice (ADR-0013), and the operator-only pane shown when an agent stalls (ADR-0007). It is a closed list pao controls, not a flag a source can set, so the exemption cannot be used to route around quiet hours. The door's quiet hours, rate-limit, and coalescing apply to everything else pao sends proactively.

3. **Budget is tracked per account, and the signal fits the method.** Agents do not all share one subscription: each agent's profile names its backend and the account it draws on (ADR-0011), so several agents may share one subscription account while others are on a second, on a different backend's subscription, or on a pay-per-use API. pao groups agents by the account they draw on and reads each account's own signal: a subscription account's utilization window (the source ADR-0007 reads for usage), or a pay-per-use account's credit or spend. It is not one global token or money count.

4. **As an account's budget fills, pao sheds the low-priority work on that account first.** A degradation ladder applied per account: background and low-priority agents drawing on a filling account are shed or paused before interactive work on that account, and concurrency caps bound how many run at once, so each account is spent on what the human is actually waiting on.

5. **The gate is the primary control; budget is the backstop.** For a subscription account there is no per-call cost, so the per-action gate (ADR-0004) governs what runs; for a pay-per-use account budget also guards spend. Either way budget keeps an account from being exhausted, it does not price individual actions.

## What this ADR deliberately does NOT decide

The notifier's exact quiet-hours, rate-limit, and coalescing parameters. The precise degradation ladder (which agent classes are shed in what order) and the concurrency-cap numbers. How "background" and "low-priority" are marked on an agent. How an account is declared on a profile and how agents are grouped by it (ADR-0011). The usage- or spend-reading mechanism itself (ADR-0007). How a shed or paused agent is resumed (relates the control plane and the still-open agent-pause question). Where the notifier runs when secondaries post directly to channels (ADR-0013), one logical policy versus a per-node door: on the single-node MVP it is one door, and the multi-node composition is deferred.

## Consequences

The human gets a managed outbound stream instead of a flood, with the must-not-wait class, an approval or a failover notice, always getting through. Each account is protected on its own by shedding background work before interactive work on it rather than by metering every call, which fits a mix of flat-rate subscription accounts (where the gate already governs what runs) and pay-per-use accounts. Keeping the notifier and the budget as one door and one per-account signal means a later change to quiet hours or to the ladder is one place, not scattered across sources.
