# ADR 0000: Record architecture decisions

**Status:** Accepted (2026-11-12)

## Context

pao is designed fully before any code is written. We want the reasoning behind each significant decision to be durable and reviewable, not lost in chat history.

## Decision

We use Architecture Decision Records. Each ADR is a numbered Markdown file in `adr/` with **Status, Context, Decision, Consequences**. ADRs are **immutable once Accepted**; a later ADR can supersede an earlier one.

An ADR records **only what is settled and durable**. Open mechanism details are deferred to following ADRs rather than finalized prematurely, so an accepted ADR does not need editing later. Broad decisions come first; detail is added in successive ADRs.

## Consequences

Decisions are auditable and onboardable. Changing a decision is itself a recorded, deliberate act.
