# ADR 0004: The approval gate

**Status:** Proposed (2026-11-13)

## Context

pao drives coding agents that run real, side-effecting tools: writing files, shell commands, network calls, sending messages. Some of those actions are safe and some are not reversible. pao needs one place to decide, before an action runs, whether it may proceed. ADR-0001 fixed that every capability, including a judge, is a routable agent; this ADR fixes how actions are gated.

## Decision

1. **pao intercepts each agent action at the tool-call boundary and decides allow / ask / deny before it runs.** The coding-agent harness exposes a pre-tool-call hook (Claude Code's is `PreToolUse`); the hook calls pao and blocks until pao answers.

2. **The decision is policy-driven: the action (tool plus arguments) maps to a risk level** such as read, private, shared, public, or destructive. A low-risk action (a read) auto-allows; a higher-risk one (shared, public, destructive) asks the operator. Deterministic rules run first.

3. **Fail-closed.** An action that matches no rule, or whose risk cannot be determined, defaults to ask, never to a silent allow.

4. **The gate mechanism is per-backend and declared in the agent profile.** Each harness states how its pre-tool-call hook works and how pao answers it (part of the agent profile, ADR-0001). This is why the gate extends to a second backend without changing the core.

5. **Fuzzy cases route to a judge agent.** When the policy rules give no clear verdict, pao routes the decision to a judge agent for one (ADR-0003: a judge is an ordinary routing target), instead of always falling back to the operator.

## What this ADR deliberately does NOT decide

The concrete risk taxonomy and policy schema; how an attended session and an unattended one differ in strictness; the judge agent's prompt and how its verdict is phrased; gate timeouts and what a timeout means; and how a denied action is surfaced back to the agent so it stays actionable. Each is deferred to its own later ADR or to implementation.

## Consequences

Every side-effecting action passes one gate with one policy, auditable in one place, and the default under uncertainty is to ask rather than to act. The gate is not special infrastructure: its hard cases are answered by a judge agent like any other routed decision, and a new backend joins by declaring its hook in its profile.
