# ADR 0030: The governor, a natural-language front-end for operator commands

**Status:** Accepted (2026-11-21)

**Superseded-by:** ADR-0031 (the governor, shared per node and started on demand), on its acceptance

Extends ADR-0026 (operator commands from chat), ADR-0027 (action buttons on a message), ADR-0023 (agent I/O and the default agent), ADR-0009 (channels), ADR-0004 (the approval gate).

## Context

ADR-0026 gives the operator a strict command surface in chat: `/pao` addresses an agent, binds output, sets a default, starts and stops agents, answers the gate. The syntax is exact and deterministic, which is what a control plane needs, but it asks the operator to know the command set and phrase each call precisely. The operator wants instead to type what they mean, for example "run an agent for the acme project", and have pao work out that this is an agent start with the acme profile, confirm the folder it matched, and issue the command. That convenience must not weaken the determinism ADR-0026 rests on: a natural-language layer uses a model, a model can misread an instruction, so it must never be the thing that decides a control action.

## Decision

1. **The governor is a translator, not an authority.** It is an always-on pao agent whose only job is to turn a natural-language operator request into one exact ADR-0026 command. It performs no node operation itself. What it produces runs through ADR-0026's own checks exactly as if the operator had typed it: a steering command is checked by the operator's access (ADR-0026 point 2), not by the agent gate of ADR-0004, which sits only on an agent's own tool calls; a translated answer to a gate question is the operator's own answer that resolves it (ADR-0004 point 6). Its own grants are bound to this role: read access limited to what point 4's matching needs, and no steering or lifecycle operation, so it cannot act on the node itself or reach an operation through pao's MCP surface (ADR-0016) in place of the commit. What it holds beyond that is the proposal, which is text.

2. **The strict surface stays authoritative and directly callable.** ADR-0026's `/pao` syntax is unchanged and remains the deterministic path. The governor is opt-in ergonomics on top of it. When determinism matters the operator uses the strict form and skips the governor, because a model can misparse.

3. **The governor proposes; the operator commits.** The governor's output is the exact `/pao` command it has derived, shown back to the operator for confirmation, not run silently. The operator commits it by sending the shown command, or by a commit button that pao, not the governor, draws from that exact command and labels with it (ADR-0027 point 4: a press runs the command as the operator who pressed it). Only then does it run, under the operator's own subject and ADR-0026's checks. A misparse is therefore visible and catchable before anything happens, and the governor cannot act on the operator's behalf without an explicit commit.

4. **It resolves and confirms the ambiguous parts before it proposes.** The governor may run the fuzzy matches a request implies, such as a profile or working-directory name against the ones that exist, and ask the operator the clarifying questions ("use the acme folder?") needed to fill a command's arguments. It reads under its own bound grants (point 1), not as the requesting operator. What a single shared governor may therefore surface, and whether that could exceed what one operator sees or carry one operator's request into another's turn, follows the per-node or per-operator choice left open below.

5. **A cheap, bounded model is enough.** The job is parse, match, confirm, emit, with the authoritative command set and every check living elsewhere, so the governor runs on a small model. It is an ordinary pao agent with a profile (ADR-0011, ADR-0015), configured with an inexpensive backend, and it is subject to the gate and the access matrix like any agent.

## What this ADR deliberately does NOT decide

- How the governor is invoked: a dedicated command such as `/pao do <text>`, versus plain text on a surface set aside for it. ADR-0026 point 1 answers an unrecognized `/pao` with usage and does not pass it on, so routing plain `/pao` prose to the governor would need its own explicit form.
- Whether an unambiguous, non-gated, clearly low-risk translation may run on one confirm or none, rather than the propose-then-commit default of point 3.
- Whether there is one governor per node or one per operator, which settles the visibility model of point 4: one per operator confines it to that operator's own view when that governor's grants are no wider than its operator's, while one per node needs a rule such as reading as the requester so a shared governor shows no operator more than they may see. And whether non-operator subjects may use it for the agents they already reach.
- Which commands it may translate: the whole ADR-0026 steering set, or a first subset.
- The model and cost ceiling, and the confirmation layout per platform.

## Consequences

The operator can drive pao in plain language without memorizing the command set, while every actual control action still takes the exact, checked ADR-0026 path. Because the governor only ever proposes a command the operator commits, a model misparse costs a visible wrong suggestion, never a wrong action, and the governor carries no authority a confused or stolen model could spend. The strict syntax stays the ground truth and the fast path, and the cost is one extra confirm step on the convenience path plus a small always-on agent.
