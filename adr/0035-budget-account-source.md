# ADR 0035: The budget figure, the unmetered account, and the unreadable one

**Status:** Accepted (2026-11-21)

Extends ADR-0014 (per-account budget). Builds on ADR-0015 (an agent has one profile, point 1, naming its one account, point 3) and ADR-0007 point 4 (health signals, usage and budget among them, are read from the backend's own source, not the agent).

## Context

ADR-0014 tracks budget per account and reads each account's own signal; ADR-0015 gives an agent one profile and so one account (point 1) and puts the account binding in the profile (point 3); ADR-0007 point 4 reads usage and budget from the backend's own source rather than the agent. What none of them fixes is what the budget figure *is*, and what happens where it does not exist: whether pao may fall back to a figure it estimates, what a metered account with no readable figure does, and the account that has no budget at all. The task dispatch of the next ADR routes work only to an account with headroom, so these have to be pinned down.

## Decision

1. **The budget figure is the account's own limit and remaining, never a figure pao estimates.** pao reads the provider's authoritative figure for the account by its method: a subscription account's utilization window, a pay-per-use account's credit balance or its rate-limit remaining. It does not approximate a budget from token or message counts, and there is no estimate fallback. This sharpens ADR-0007 point 4 from "not self-reported" to "the provider's own limit, not a figure pao computes."

2. **An account is metered or unmetered; a local or self-hosted model is unmetered.** An unmetered account has no budget: its agents are never shed or paused for budget. An account exposes a figure by whatever method it has, and a rate-limit remaining with no credit balance is still a figure; "no budget" means declared unmetered, not "no credit line."

3. **A metered account whose figure cannot be read is a fault, not a guess and not unlimited.** When a metered account exposes no authoritative figure at all, pao surfaces the fault to the operator and dispatches no new budget-gated work to that account; work already running on it is not killed, and it clears when the figure reads again. Unreadable means no signal by any method, distinct from an unmetered account, which is a deliberate declaration.

4. **The figure is live remaining headroom, not a static cap.** What pao reads and acts on is how much the account has left, so the ADR-0014 shed ladder works against real remaining capacity and an operator can be answered "what is left on this account" from the source.

## What this ADR deliberately does NOT decide

The per-provider reader: the endpoint and the window, credit or rate arithmetic stay ADR-0007's mechanism, one per account method, added as providers are. Where metered-ness and the unmetered flag are declared (the ADR-0015 profile account field, or the orchestrator config that holds the account's credential): that placement is deferred. How often pao reads versus caches the figure, and how stale a cached figure may be. The shed ladder's classes and order and the concurrency caps (ADR-0014 defers these). How a faulted account recovers beyond "no new dispatch until it reads again." Multiple accounts per agent is not left open: ADR-0015 points 1 and 3 give an agent one profile and one account.

## Consequences

Budget rests on the provider's own limit and remaining, so it cannot drift from reality the way an estimated count would, and a missing figure is a visible fault rather than a silent guess or a silent free pass. A local model carries no budget and is never throttled for one. A rate-limit remaining bounds an account's throughput, not its spend, so a pay-per-use account that exposes only a rate limit and no credit figure has its throughput bounded but no spend cap, and spend on it rests on the per-action gate (ADR-0014 point 5) rather than a budget guard. Because the figure is the live remainder, "what is left on this account" is answerable from the source, which is what the task dispatch of the next ADR needs to route work only to an account with headroom.
