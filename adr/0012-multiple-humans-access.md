# ADR 0012: Multiple humans and tag-scoped access

**Status:** Proposed (2026-11-13)

## Context

ADR-0005 fixed access as an explicit, default-restricted matrix whose rows are agents and columns are the resources they may reach, administered on pao's management surface. pao also serves more than one human, and those humans must not all have the same reach: one operator with everything, others with a limited slice. This ADR extends the access model to multiple humans and to tag-scoped grants. It builds on ADR-0005, which stays in force; the agent matrix of ADR-0005 is the agent case of the general subject matrix here.

## Decision

1. **Humans are subjects in the access matrix, alongside agents.** ADR-0005's rows were agents; a human is the same kind of subject, so the matrix governs which resources (agents, tools, channels) each human may reach, with the same default of no access. There is more than one human, and each has their own row.

2. **A human is leveled by the tags granted to it.** The operator is the super-admin, with everything. Another human is granted a set of tags and reaches exactly what those tags cover, nothing more.

3. **Tags are access scopes, and they are the same tags used for routing.** Every resource carries tags (the name-and-tags of ADR-0003). A tag is a scope: granting a subject a tag grants access to every resource carrying that tag, so one grant covers a whole group of resources rather than a cell at a time.

4. **A grant may target a tag-scope or a single cell; the tag layer sits on top of the matrix.** Tag-scoped grants do not replace ADR-0005's per-cell grants; they are a layer over the same matrix. A subject's access is the union of its tag-scopes and any individual cells. This holds for both humans and agents.

5. **Access is per-subject and may be mutually exclusive.** A resource one subject reaches can be out of reach for another, the operator included, so one subject's private resource stays private from the rest.

## What this ADR deliberately does NOT decide

Whether an agent is owned by a single human at a time and how such control is handed off (a separate, still-open question). The tag taxonomy and how tags are attached to resources. The matrix storage, schema, and the management surface (ADR-0005). How a grant is edited (ADR-0005's web app and API). Enforcement and audit: the per-action axis is the gate (ADR-0004) and the record of a decision is the audit-log ADR.

## Consequences

pao serves several humans without giving them equal reach: each is a subject with its own scope, so a limited user gets exactly the slice granted and nothing else, and one human's resources can stay invisible to the others. Tags as scopes make a grant a single act over a group rather than a cell-by-cell chore, and reuse the routing tags rather than a second vocabulary. Because this extends ADR-0005's matrix rather than replacing it, the agent rules there keep holding unchanged.
