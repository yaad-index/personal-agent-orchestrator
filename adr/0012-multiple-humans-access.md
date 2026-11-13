# ADR 0012: Multiple humans and tag-scoped access

**Status:** Accepted (2026-11-13)

## Context

ADR-0005 fixed access as an explicit, default-restricted matrix whose rows are agents and columns are the resources they may reach. pao also serves more than one human, who must not all have the same reach. This ADR extends that model to multiple humans and to tag-scoped grants. It only adds to ADR-0005, which stays fully in force; it does not change or partially supersede it. The agent matrix of ADR-0005 is the agent case of the general subject matrix here.

## Decision

1. **Humans are subjects in the access matrix, alongside agents.** A human is the same kind of subject as an agent (ADR-0005), with its own row, the same resource columns (agents, tools, channels), and the same default of no access. There is more than one human.

2. **A subject's access is only what it is granted; there is no blanket access.** Access is the union of a subject's grants over the default-deny of ADR-0005. Nothing is reachable except by a grant, so a resource is kept private simply by not granting it to others. This holds for every subject, the super-admin included: the super-admin administers the matrix but is not itself automatically granted every resource, so a resource can be out of even the super-admin's reach.

3. **Tags are access scopes, and they are the routing tags.** A tag is granted as a scope: granting a subject a tag grants access to every resource carrying that tag, so one grant covers a group rather than a cell at a time. It is the same tag a resource is routed by, generalized from the agent name-and-tags of ADR-0003 to tools and channels as well. The coupling is deliberate: a routing tag and an access scope are one thing.

4. **Addressing is bounded by the requester's access, by tag or by name.** Because a tag is also a scope, a fan-out to a tag reaches only the resources the requester is actually granted, never past its scope. Addressing a specific resource by name that the requester is not granted is not distinguishable from addressing one that does not exist, so neither a tag nor a name can be used to probe for resources out of reach.

5. **A grant may be a tag-scope or a single cell.** A subject's access is the union of its tag-scopes and any individual cells, for humans and agents alike.

## What this ADR deliberately does NOT decide

Whether an agent is owned by a single human at a time and how such control is handed off (still open). How an inbound sender is resolved to a particular human subject (identity). What a channel grant means for a human beyond a front it may use, and the precise semantics of a tool or channel grant. Any access expressed as an exception ("all but X") beyond additive grants. How tags are attached to resources, the tag taxonomy, and the matrix storage, schema, and management surface (ADR-0005). The per-action gate (ADR-0004) and the audit record (the audit-log ADR) are separate axes. The term "operator" in ADR-0004 and ADR-0007 remains this single super-admin; the additional humans are non-operator subjects.

## Consequences

pao serves several humans without equal reach: each is a subject whose access is exactly what it is granted, so a limited user gets its slice and nothing more, and a resource granted to one human alone stays private from the rest, the super-admin included. Tags as scopes make a grant one act over a group and reuse the routing vocabulary, with fan-out bounded by the grant. Because the model only adds subjects and a tag layer to ADR-0005's matrix, the agent rules there keep holding unchanged.
