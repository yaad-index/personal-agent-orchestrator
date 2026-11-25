# ADR 0043: The memory driver, storing and retrieving what an agent or template remembers

**Status:** Proposed (2026-11-25)

## Context

ADR-0039 named memory as driver plane 2, and noted pao has no memory of its own today. ADR-0041 made a template carry persistent memory and deferred to this ADR whether that memory is per-user or shared, leaving the binding of a template's memory to the tag-as-capability ADR that follows.

This ADR draws plane 2: how memory is stored and retrieved, how it is keyed and scoped, and what the first backend is. It follows ADR-0039's plane discipline, a narrow interface, a simple first implementation, and a contract test.

## Decision

1. **Memory is reached through a narrow driver, keyed by tag.** The memory plane (ADR-0039 plane 2) is a driver that stores and retrieves what an agent or template should remember. A request carries a **tag**, a capability tag such as a language or a tool area, and the driver returns the memory matching it. The tag is the key here; its definition as the capability primitive is the next ADR's subject, and this ADR uses it only as the retrieval key.

2. **Memory is namespaced and may be per-user or shared, under the access model.** Each memory lives in a namespace and carries its tags. A namespace may be scoped to one user, private to that user's orchestrator (ADR-0040), or shared across users and agents. What a reader may retrieve is bounded by ADR-0012 point 2's default-deny, so a shared namespace grants no blanket access and never returns one subject's private memory to another without a grant. This settles ADR-0041's deferred question: both per-user and shared namespaces exist, and the access model decides what any reader sees. Memory is addressable across the mesh (ADR-0001), not pinned to the node an agent runs on; the cross-node transport is ADR-0017's.

3. **The simple first implementation needs no external dependency.** The first backend is a local store pao can run on its own, a filesystem plus a simple index, enough to store, tag, namespace, and retrieve. Per ADR-0039, a mature external memory backend, for example Honcho, may later replace it behind the same interface as configuration, never baked into the core, and this ADR names none as mandatory. Each backend passes the plane's contract test and declares what it cannot do (ADR-0039's declare-so stance).

4. **A cleanup tool runs over memory.** Memory accumulates, so the plane provides a cleanup tool that prunes or compacts it under rules, run on demand or on a schedule. Where the data-source round-trip of ADR-0042 keeps history, memory needs the opposite pressure, a way to shed what is stale, so the two planes are deliberately asymmetric.

The internals left open: the retrieval semantics beyond a tag match (ranking, recency, scope precedence); the namespace scheme and how a template's memory namespace is derived from its tags, settled with the tag-as-capability ADR; the cleanup rules and their defaults; and the exact contract-test shape for a memory backend.

## Consequences

Memory becomes a swappable plane rather than a built-in: the same agents and templates run on a local filesystem store or a mature external backend by configuration, and what they remember is keyed by tag and bounded by the access model, so memory follows the same grant discipline as every other resource. A cleanup tool keeps it from growing without bound. This **builds on ADR-0039** (plane 2 and the driver discipline), **ADR-0041** (a template carries memory, whose per-user-or-shared deferral this settles), **ADR-0012** (the access model that bounds retrieval), and **ADR-0040 and ADR-0001** (the per-user orchestrator a private namespace belongs to, and the mesh memory is addressable across); it supersedes none, and it leaves the tag's own definition to the next ADR.

The cost is a memory interface that must serve both a local store and a mature backend without leaking either's shape, plus a cleanup design that prunes without dropping what is still wanted. The mitigation is ADR-0039's simple-first discipline: prove the local filesystem store before an external backend is drawn.
