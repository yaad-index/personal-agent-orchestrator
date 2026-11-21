# ADR 0035: The governor knows what exists, never a secret

**Status:** Proposed

Supersedes ADR-0034 (the governor replies where it was asked). Keeps its points 2 to 4 and 6 to 8, its deferred list and its consequences, except where noted below. Changes point 1's bound on what the governor reads, and replaces point 5. This continues point 4's chain: ADR-0030, then 0031, 0032, 0034 and this, each superseding the one before in full. Extends ADR-0012 (multiple humans and access) and ADR-0023 (channels).

## Context

ADR-0031 point 5 scoped every read the governor makes to the requester: a read was allowed only when both the governor's grants and the requester's allowed it. The aim was that a shared governor shows no operator more than they could see themselves.

That made the governor worse at its one job. To translate "bind the coder to the team group", it needs to know which agents, groups and channels exist, and it could not see any channel at all. This is a personal orchestrator, not a secret manager. That something exists, and its shape, are not what needs protecting. What needs protecting are secrets and credentials, and what an operator may do.

What an operator may do is already enforced where it counts. A proposal runs only when an operator commits it, as that operator, under their own checks (ADR-0026, ADR-0027 point 4). The governor knowing a name grants nothing.

## Decision

1. **The governor is a translator, not an authority.** As before, it turns a natural-language operator request into one exact ADR-0026 command, and performs no node operation itself. What it produces runs through ADR-0026's checks, exactly as if the operator had typed it.
   - **Changed: it reads what exists, by name and shape.** That covers the node's agents, recipes and channels, with each one's configuration as far as matching needs: names, tags, kinds, state, a channel's admitted groups and its member policy. It does not need to be told what any one operator may use.
   - **Never a secret.** No credential, token, key, or the name of the variable that holds one, and no channel option, endpoint or adapter setting reaches the governor through any read. A read is built from an allowed list of fields, never by removing secrets from a whole configuration.
   - **Still no action.** It holds no steering or lifecycle operation, so it cannot act on the node, or reach an operation through pao's MCP surface (ADR-0016) in place of the commit. Only its reads are widened.

5. **One governor per node, shared by its operators; it reads under its own grants.** Replaced.
   - The governor's reads are checked against its own grants alone, whoever it is serving. They are no longer narrowed to what the requester may see.
   - Channels are listed to it whole. A channel is not an access-matrix resource (ADR-0012), so no grant names one.
   - A read made outside a request still returns nothing. The governor has nothing to match then.

## How this meets the rules on hiding what exists

- **ADR-0012 point 4.** Addressing an agent out of the requester's reach by name is still indistinguishable from addressing one that does not exist. The rule is about addressing, and that is unchanged: a command the governor proposes for such an agent is answered "not found" when it is run.

  But the governor can now name such an agent in a clarifying question or a proposal. That is a path around point 4's intent, that a subject cannot learn what is out of its reach. This ADR takes that path deliberately, for the governor only. Any agent's output can already say whatever that agent knows. The governor is now allowed to know what exists.
- **ADR-0024 point 4.** It is unaffected. The governor is offered no proxied tool at all (point 1: no action), so its tool list reveals nothing of the tools an operator is granted.
- **Group replies (point 4).** The governor's output in a group can now name things a reader may not reach, not only things the requester sees. Today every reader of an admitted group is an operator, and all of them decide as the one operator subject. They see the same, but not necessarily what the governor's broader grants reach. This ADR accepts that, for the reason above.

## What this ADR deliberately does NOT decide

- Everything ADR-0034 left open stays open.
- Which further reads matching may need later, such as the standing bindings for proposing an unbind. Each joins point 1's list by name, under the same rule against secrets.

## Consequences

- The governor can match a channel, a group and an agent the requester never named exactly, and propose the full command.
- An operator may learn that an agent, recipe or channel exists although their own access does not reach it. They still cannot act on it.
- No secret reaches the governor, so none can reach its context, its replies or a group it answers in.
- A proposal for something the presser cannot reach fails when committed, with "not found", as typing it would.
- ADR-0034 is superseded in full. Its points 2 to 4 and 6 to 8, its deferred list and its consequences carry over. Point 1's read bound is changed and point 5 is replaced, as above.
