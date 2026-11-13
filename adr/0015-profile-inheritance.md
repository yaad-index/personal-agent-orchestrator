# ADR 0015: Profile inheritance and the account

**Status:** Accepted (2026-11-13)

## Context

ADR-0011 fixed that each agent has a profile declaring its per-backend specifics. In practice profiles overlap heavily (many agents on the same backend differ only in model or account), and pao needs to know which account each agent draws on, the account binding ADR-0014 required and deferred. This ADR extends the profile with inheritance, the model, and the account field. It builds on ADR-0011, which stays in force.

## Decision

1. **Each agent resolves to exactly one profile.** An agent has one profile, the single statement of where it comes from: its backend and the per-backend specifics of ADR-0011, plus two fields this ADR adds, its model and the account it draws on.

2. **Profiles inherit; the name is never inherited.** A profile may inherit from another and override only what differs: a base profile holds a backend's common settings, and a specialized profile that inherits it sets, say, the model or the account. An agent's effective profile is its own fields resolved over its inheritance chain. The agent's name (ADR-0003) is per-agent and never inherited, since a name addresses one specific agent. How tags (ADR-0003) combine over a chain is deferred; and because a tag is also an access scope (ADR-0012), that resolution rule must account for inheritance changing which subjects can reach the agent.

3. **The profile names the account the agent draws on.** Which subscription or API identity an agent uses is a profile field, so budget (ADR-0014) and anything else can group agents by it. This is the binding ADR-0014 required and deferred; it lives here.

4. **The profile names the account, not the secret.** The profile says which account an agent uses; the credential for that account stays in the orchestrator config (ADR-0001's two-config split, the agent-intrinsic profile versus orchestrator-held credentials), so a shared or public profile never carries a secret.

## What this ADR deliberately does NOT decide

The inheritance resolution rules in detail (whether a profile may inherit from more than one, conflict order, how deep a chain may go). Which fields may be overridden versus fixed. How tags combine over an inheritance chain (point 2), with its access consequence (ADR-0012). How an account maps to its credential in the orchestrator config. The profile's storage and its exact schema, both still open. It does not change ADR-0011's existing fields; it adds inheritance, the model, and the account on top.

## Consequences

Profiles stop repeating themselves: a backend's common settings sit in one base profile and a per-agent profile is just its differences, so a new agent on an existing backend is a short profile. pao can group agents by account for per-account budget (ADR-0014) because the account is a first-class profile field. Keeping the account in the profile but the secret in the orchestrator config means a profile stays shareable. Because this only adds to ADR-0011, the existing per-backend fields keep holding.
