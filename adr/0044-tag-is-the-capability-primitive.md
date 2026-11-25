# ADR 0044: The tag is the single capability primitive

**Status:** Accepted (2026-11-25)

## Context

The tag already does two jobs: it is a routing key (ADR-0003 point 2, a tag is a capability or role an input can address) and an access scope (ADR-0012 point 3, a resource carries a tag and granting a subject a tag grants access to every resource carrying it; ADR-0024 point 7 has the operator assign a proxied tool's tags for exactly this). But the rest of what a capability needs is scattered: a skill, the tools and MCP (ADR-0024), command grants and resource access (ADR-0012), a secret's access (ADR-0015, ADR-0037), and a memory namespace (ADR-0043). To give an agent a capability today, several of these are set in several places.

ADR-0041 deferred how a template's access and memory are expressed and bound, and ADR-0043 deferred which namespace a template's memory derives from, both to this ADR. This ADR makes the tag the single primitive that draws all of it together, and it does so **by widening what carries a tag**, not by adding a second access mechanism.

## Decision

1. **The tag is the single capability primitive, by widening what carries a tag.** A tag is already the routing key (ADR-0003) and an access scope that reaches every resource carrying it (ADR-0012 point 3). This ADR widens the kinds of resource that can carry a tag so one tag draws a whole capability together: a skill, the tools and MCP, command grants, a secret's access, and a shared memory namespace can each carry a tag. Assigning a subject or a template that tag reaches all of them, exactly as ADR-0012 point 3 already reaches every resource carrying a tag. The tag is **not** a separate definition listing a closed set; the capability is whatever carries the tag, so ADR-0012 point 3 and ADR-0024 point 7 are kept, not narrowed.

2. **What a capability's tag can carry**, each an ordinary tagged resource under ADR-0012 point 3:
   - a **skill**: new, pao-provided capability material, an instruction or knowledge file for the capability. It is **not** one of ADR-0025's host-user context categories (`instructions`, `skills`, `settings`, `mcp`, `memory`), and a tag opens none of those, so ADR-0025 point 1's default-off host context stays shut.
   - the **tools and MCP** it uses (ADR-0024, with the operator assigning a proxied tool's tags per ADR-0024 point 7).
   - **command grants**, such as shell access, as matrix grants (ADR-0012).
   - a **secret's access**: the tag grants the **right to resolve** a task's secret reference (ADR-0037 point 3, which extends ADR-0015 point 4's account/secret split), never a standing value and never another account's backend credential; resolution still happens only at a run whose task references the secret, for that run alone (ADR-0037 point 3), and the secret-run rules of ADR-0042 point 5 and ADR-0043 point 4 still apply.
   - a **shared memory namespace** (ADR-0043). A **per-user** namespace is not handed out through a tag (ADR-0043 point 2 grants it to the user and their orchestrator); a per-user namespace that carries a tag resolves to the **user of the subject** the tag is assigned to, and for a template, which has no user of its own, at the **instance** that runs for a user, never to one fixed user's namespace.

3. **Tag-as-grant and tag-as-retrieval-key are two roles of the same tag, not a contradiction.** Here a tag grants access to the resources carrying it (ADR-0012 point 3); in ADR-0043 the same tag is the retrieval key within the memory namespaces a reader is already granted. The grant decides what is reachable; the key filters within it. ADR-0043 point 1's "a tag does not reach across a namespace boundary" is about retrieval and holds alongside this grant role.

4. **A tag grants its pieces but bypasses no gate, and tagging a resource is itself gated.** Assigning a tag is a grant over ADR-0012 point 2's default-deny, and every piece is still checked at use by the gate (ADR-0004); the tag grants access, it exempts no call. **Tagging or untagging a resource needs a grant and passes the gate** (ADR-0005 point 2, ADR-0004), since that is the act that puts a resource into a tag's reach and a tag is powerful; who holds that grant is left open.

5. **A tag may be assigned to a human subject (ADR-0012 point 1).** The pieces that are agent-run material, a skill to run or a memory namespace read as an agent, have no meaning for a human and do not apply; a human's tag grants the access pieces it is a subject for, the tools, resources and command grants, as ADR-0012 already grants a human a tagged resource.

6. **This settles where a template's access and memory bind** (ADR-0041, ADR-0043): a template carries tags, and its access and its memory are what those tags carry; an instance inherits its template's tags (ADR-0041 point 3) and so the capability.

The internals left open: who holds the grant to tag or untag a resource, and how a tag's membership is recorded; how tags combine over a profile inheritance chain (ADR-0015 point 2 defers this and warns inheritance changes which subjects reach an agent, now widened to a whole capability); how a per-user namespace carrying a tag resolves to the assignee's user; the tag taxonomy; and the migration of today's scattered per-category and per-cell grants onto tags.

## Consequences

There is one primitive to reason about and to grant, not several scattered mechanisms: giving a capability is assigning a tag, and routing, access, skill, tools, secret access and memory follow it because they carry it. The capability is open, whatever carries the tag, which is why this does not disturb ADR-0012 point 3 or ADR-0024 point 7. This is what lets a template, and the ephemeral instances made from it (ADR-0041), carry their whole capability set by tag, and it closes the binding ADR-0041 and ADR-0043 left open. This **builds on ADR-0003** (the tag as routing key), **ADR-0012** (the tag as access scope and the default-deny a grant sits over), **ADR-0024** (the tools and MCP, and operator-assigned tool tags), **ADR-0025** (whose host-user categories a tag does not open), **ADR-0015 and ADR-0037** (a secret's reference and its per-run resolution), **ADR-0042 and ADR-0043** (the secret-run rules and the memory namespace), **ADR-0041** (a template's tags), and **ADR-0005 and ADR-0004** (the grant and gate that bound and define a tag); it supersedes none.

The cost is that a tag becomes a powerful object: a mis-set tag over-grants across every piece carrying it at once, so tagging or untagging a resource is itself a gated, access-controlled act, and the default-deny and the per-use gate are what keep the breadth safe. The other cost is the migration of today's scattered grants onto tags, left open above rather than forced here.
