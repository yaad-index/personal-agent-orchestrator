# ADR 0004: The approval gate

**Status:** Accepted (2026-11-13)

## Context

pao drives coding agents that run real, side-effecting tools: writing files, shell commands, network calls, sending messages. Some of those actions are safe and some are not reversible. pao needs one place to decide, before an action runs, whether it may proceed. ADR-0001 fixed that every capability, including a judge, is a routable agent; this ADR fixes how actions are gated.

## Decision

1. **pao intercepts each agent action at the tool-call boundary and decides allow / ask / deny before it runs.** The coding-agent harness exposes a pre-tool-call hook (Claude Code's is `PreToolUse`); the hook calls pao and blocks until pao answers.

2. **The decision is policy-driven: the action (tool plus arguments) maps to a risk level** such as read, private, shared, public, or destructive. A low-risk action (a read) auto-allows; a higher-risk one (shared, public, destructive) asks the operator. Deterministic rules run first.

3. **Fail-closed.** An action that matches no rule, or whose risk cannot be determined, is never silently allowed; it is held and resolved through the escalation chain in points 5 and 6.

4. **The gate mechanism is per-backend and declared in the agent profile.** Each harness states how its pre-tool-call hook works and how pao answers it (part of the agent profile, ADR-0001). This is why the gate extends to a second backend without changing the core.

5. **An uncovered or fuzzy action goes to a judge agent first.** When the policy has no matching rule, or the matching rule gives no clear verdict, pao routes the decision to a judge agent (ADR-0003: a judge is an ordinary routing target) rather than defaulting straight to the operator.

6. **The operator is the final fallback, and the policy grows from their answers.** When the judge is also inconclusive, pao asks the operator. On the operator's approval, pao can record that action into the policy so the same request is covered next time without judge or operator. The policy is accruing data, not a fixed table baked into the code.

## What this ADR deliberately does NOT decide

The concrete risk taxonomy and policy schema; how an attended session and an unattended one differ in strictness; the judge agent's prompt and how its verdict is phrased; gate timeouts and what a timeout means; how a denied action is surfaced back to the agent so it stays actionable; and the policy as a mechanism, meaning where and how it is stored, how pao reads it, and how it grows (how an addition is proposed, decided and recorded, point 6). Each is deferred to its own later ADR or to implementation.

## Consequences

Every side-effecting action passes one gate with one policy, auditable in one place, and the default under uncertainty is to ask rather than to act. The gate is not special infrastructure: its hard cases are answered by a judge agent like any other routed decision, and a new backend joins by declaring its hook in its profile.
