# ADR 0044: The tag is the single capability primitive

**Status:** Proposed (2026-11-25)

## Context

The tag already does two jobs: it is a routing key (ADR-0003 point 2, a tag is a capability or role an input can address) and an access scope (ADR-0012 point 3, granting a subject a tag grants access to every resource carrying it, and a routing tag and an access scope are one thing). But the rest of what a capability needs is scattered: the skill, an instruction file, is a profile category (ADR-0025 point 3); the tools and MCP are configured in pao (ADR-0024); command grants and resource access are matrix cells (ADR-0012); a secret is reached through the account on a profile (ADR-0015, ADR-0037); and the memory namespace is plane 2's (ADR-0043). To give an agent a capability today, several of these are set in several places.

ADR-0041 deferred how a template's access and memory are expressed and bound, and ADR-0043 deferred which namespace a template's memory derives from, both to this ADR. This ADR makes the tag the single primitive that carries all of it.

## Decision

1. **The tag is the single capability primitive.** Beyond routing (ADR-0003) and access scope (ADR-0012 point 3), a tag is the one handle that bundles everything a capability needs. Assigning a subject or a template a tag grants that whole bundle: everything is controlled by tag.

2. **What a tag bundles.** A tag's definition names, for that capability, the pieces that today live apart:
   - the **skill**, the instruction or knowledge file for the capability (the `skills` category of ADR-0025 point 3);
   - the **tools and MCP** it uses (ADR-0024, ADR-0016);
   - the **command grants** it needs, such as shell access, as matrix grants (ADR-0012);
   - the **secrets and resource access** it requires (ADR-0015, ADR-0037);
   - the **memory namespace** it reads and writes (ADR-0043).
   One tag, one bundle, declared once against the tag and applied wherever the tag is assigned.

3. **The tag is where a template's access and memory bind.** This settles the deferral of ADR-0041 and ADR-0043: a template carries tags, and each tag brings its bundle, so a template's access and its memory namespace are the union of its tags' bundles, declared on the template as tags rather than hand-built. A task-agent instance inherits its template's tags (ADR-0041 point 3) and so the bundles; the memory namespace a template reads is the one its tags name.

4. **A tag grants its bundle but bypasses no gate.** Assigning a tag is a grant under the access matrix, over ADR-0012 point 2's default-deny: nothing is in the bundle that the tag's definition does not name, and nothing outside it is reached. Each piece of a bundle is still checked at use by the gate (ADR-0004); the tag grants the bundle, it does not exempt any call in it. A tag whose bundle a backend or driver cannot honor declares so (ADR-0039's stance), rather than silently granting less.

The internals left open: the tag-definition format and where a definition lives; who may define or change a tag, which is itself an access-controlled act since a tag's definition is powerful; how overlapping tags compose, union and any precedence; the tag taxonomy; how a tag's secret reference resolves at run (ADR-0037); and the migration of today's scattered per-category and per-cell grants onto tags.

## Consequences

There is one primitive to reason about and to grant, not five scattered mechanisms: giving a capability is assigning a tag, and routing, access, skill, tools, secrets and memory follow it as one bundle. This is what lets a template, and the ephemeral instances made from it (ADR-0041), carry their whole capability set by tag, and it closes the binding ADR-0041 and ADR-0043 left open. This **builds on ADR-0003** (the tag as routing key), **ADR-0012** (the tag as access scope and the default-deny a grant sits over), **ADR-0024 and ADR-0016** (the tools and MCP a tag names), **ADR-0025** (the skill category), **ADR-0015 and ADR-0037** (the secrets a tag names), **ADR-0043** (the memory namespace), and **ADR-0041** (a template's tags, whose access-and-memory binding this settles); it supersedes none, since each is extended, and the tag's new breadth does not change what any of them already decided.

The cost is that the tag-definition becomes a powerful object: a mis-set tag over-grants across every piece it bundles at once, so defining or changing a tag is itself an access-controlled, gated act, and the default-deny and the per-use gate are what keep the breadth safe. The other cost is the migration of today's scattered grants onto tags, which is left open above rather than forced here.
