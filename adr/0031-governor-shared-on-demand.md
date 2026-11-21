# ADR 0031: The governor, shared per node and started on demand

**Status:** Accepted (2026-11-21)

Supersedes ADR-0030 (the governor). Keeps its points 2 and 3, and the role and bounded grants of its point 1. Changes point 1's "always-on", point 4's rule for whose view the governor reads under, and point 5's "always-on". Settles four of the points it left open: how the governor is invoked, one governor per node or per operator, which commands it may translate, and what is configurable. Extends ADR-0006 (temporary agents), ADR-0026 (operator commands from chat), ADR-0029 (output routing).

## Context

ADR-0030 made the governor a translator. It turns an operator's plain words into one exact ADR-0026 command and shows it back. The operator commits it, or not. It left open how the governor is invoked and whether there is one per node or one per operator, and it assumed an always-on agent.

Those choices are now made. There is one governor per node, shared by its operators, invoked by its own prefix. It is not started with the node: it is started the first time it is asked. Two things in ADR-0030 do not survive this.

- **Point 4.** It has the governor read under its own grants, not as the requesting operator. That is safe when each operator has a governor whose grants are no wider than theirs. It is not safe for a governor shared by operators who see different things.
- **Points 1 and 5.** These call it always-on, which it no longer is from the node's start.

The supersession is in full, as the index requires. What is kept is said below.

## Decision

1. **The governor is a translator, not an authority.** Kept from ADR-0030 point 1, except that it is started on demand rather than with the node. It turns a natural-language operator request into one exact ADR-0026 command, and performs no node operation itself.
   - What it produces runs through ADR-0026's checks, exactly as if the operator had typed it.
   - Its grants are read-only and limited to what matching needs. It holds no steering or lifecycle operation, so it cannot act on the node, or reach an operation through pao's MCP surface (ADR-0016) in place of the commit.

2. **The strict surface stays authoritative and directly callable.** Kept from ADR-0030 point 2. `/pao` is unchanged and remains the deterministic path.

3. **The governor proposes; the operator commits.** Kept from ADR-0030 point 3. The governor's output is the exact `/pao` command, shown to the operator. The operator commits it by sending it, or by the button pao draws from that exact command (ADR-0027 point 4).
   - A node's configuration may let a command that changes nothing run on the request alone, without a separate commit. Listing agents or bindings is an example.
   - A command that changes anything is always proposed and committed. Examples: a spawn, a kill, a bind, a view change.

4. **It is invoked as `/gpao <text>`.**
   - `/gpao` is the default. A node's configuration may name another prefix in its place, as long as it is not pao's command prefix. Below, `/gpao` stands for whichever prefix the node uses.
   - Like `/pao` (ADR-0026 point 1), `/gpao` is pao's and never reaches an ordinary agent. The text after it goes to the governor alone, as a request from the sender.
   - `/gpao` is never another spelling of a `/pao` command. A command typed after `/gpao` is a request to translate, not a command.
   - A reply to the governor's clarifying question is sent with `/gpao` too. Plain text keeps its ADR-0023 routing.
   - Only operators may use it. With no governor configured on the node, the sender is told so.
   - Everything the governor says goes to the requester alone, privately, as an ADR-0026 point 5 answer does. That covers proposals, clarifying questions and refusals. This holds even when the request was sent in a group. The governor has no standing bindings (ADR-0029 point 3).

5. **One governor per node, shared by its operators; its reads follow the requester.**
   - Every read the governor makes while serving a request is checked twice: against the governor's own bounded grants, and against what the requester may see at that moment. Only what both allow comes back.
   - A shared governor therefore shows no operator more than that operator could see themselves. Its grants cap what it reads for anyone.
   - A read made outside a request, with no requester, returns nothing.

6. **One requester's request never sits in the context another requester's is answered in.**
   - When a request comes from a different subject than the one before, the governor starts with a fresh context.
   - Within one requester's exchange, the context is kept, so a clarifying question and its answer belong together.

7. **It is started on demand, and stopped only as any agent is.**
   - pao does not start the governor with the node. It starts the governor on the first `/gpao` request.
   - A request that arrives while the governor starts waits for it. If it cannot start, the requester is told.
   - Once running, the governor stays running. pao never stops it on a timer or when it is idle. It stops only by an explicit act, as any agent does under ADR-0007.
   - A `/gpao` request after the governor was stopped starts it again. A request is itself the explicit ask to use it.
   - The governor is not a temporary agent (ADR-0006). It has no ephemeral profile and no workdir to harvest. It is a configured agent of the node.

8. **What it may propose, and what the node configures.**
   - **What it may propose:** ADR-0026's steering commands, other than the gate's answers (`allow`, `always`, `deny`) and `help`. A gate question stays the operator's to answer, never a model's to suggest.
   - **Spawn and kill join the steering set.** This decision adds a temporary agent's spawn from a recipe, and its kill, to the chat steering set (ADR-0026 point 2). Both are operations the operator surface already has, with the same checks. A kill harvests first, as ADR-0006 point 4 requires, and surfaces the outputs to its sender.
   - A request such as "start a helper on this repository" is matched to the recipe whose source that is (ADR-0006 point 5).
   - **What the node configures:**
     - which profile and model the governor runs. The configuration reserves an option to point the model at any endpoint, local or external, that speaks the widely used chat-completions HTTP API. The option is reserved here and not built;
     - the commands of point 3 that may run without a commit;
     - a narrower set of proposable commands.
   - The configuration cannot add the gate's answers to that set.

## What this ADR deliberately does NOT decide

- Whether subjects who are not operators may use `/gpao` for the agents they already reach.
- A spawn from a source that no recipe names. That is not an operation pao has, and would need its own decision.
- The model and cost ceiling. These are configuration, with defaults in the configuration reference.
- How the governor reaches a model at a chat-completions endpoint. ADR-0011's backends are coding-agent harnesses, and such an endpoint is not one: pao would have to carry the tool calls itself. That needs its own decision before the reserved option of point 8 is built.
- The confirmation layout per platform.

## Consequences

- The operator drives pao in plain words with `/gpao`. Every control action still takes the exact, checked ADR-0026 path, and the strict `/pao` stays the ground truth.
- A shared governor costs one agent per node, not one per operator. It costs nothing until first asked. Once started, it runs until stopped.
- **The price of starting on demand:** the first request waits for the governor to start.
- **The price of the per-requester context:** when two operators use the governor in turn, a clarifying exchange that one of them left open is lost. That operator asks again.
- Reads follow the requester, so sharing the governor shows no one more than they may see. A misparse remains a visible wrong suggestion, never a wrong action.
- The one exception to commit-first is a command that changes nothing, and only where the node is configured for it.
- ADR-0030 is superseded in full. Its points 2 and 3 carry over unchanged, as does its point 1 apart from "always-on". Its open questions are settled here, except the two kept above: other subjects' use, and the cost ceiling and confirmation layout.
