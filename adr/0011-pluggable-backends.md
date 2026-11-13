# ADR 0011: Pluggable backends

**Status:** Proposed (2026-11-13)

## Context

ADR-0001 fixed that agent I/O is terminal-in, transcript-out, and that a per-agent profile holds the agent-intrinsic config. Several later ADRs then deferred a per-backend specific here: the transcript path and format the mirror reads (ADR-0008), the per-backend gate hook (ADR-0004), and the agent's tool interface (ADR-0010). This ADR fixes what makes a backend pluggable: the uniform contract every backend meets, and what its profile declares.

## Decision

1. **Every backend meets one uniform I/O contract.** A backend is any CLI coding agent that reads its terminal and writes a transcript (ADR-0001). pao drives them all the same way: paste in, read the transcript out. Claude Code is the first backend; others are added behind the same contract, not by changing pao's core.

2. **The agent profile declares every per-backend specific.** The profile (ADR-0001's per-agent config) carries, per backend: the input method pao uses to submit a paste; the transcript path and format the ADR-0008 mirror reads; the hook classes and what each is for (which hook is the gate hook of ADR-0004, which are turn-state hooks); the tool interface the agent exposes (the quote/id resolution and fetch-by-id of ADR-0010, including how ids are surfaced to the agent); the source for context, usage, liveness, and login expiry; and the backend's own commands (such as compact or reset). pao reads these from the profile, and nothing backend-specific is hard-coded in the core.

3. **The transcript format is guarded by the contract test.** The per-backend transcript format the profile declares is pinned by the contract test of ADR-0008, which runs the real backend CLI and fails loudly if the format changes, so a backend upgrade cannot silently break the mirror.

4. **Fresh-agent startup is a per-backend recipe with a readiness timeout that prints the pane.** Bringing a backend to a ready prompt is a recipe declared in its profile, with a timeout that prints the terminal pane if it stalls rather than enumerating every first-run modal. Any file a backend requires on first run is seeded as that backend's per-profile detail.

5. **pao's own files use generic, pao-namespaced names.** pao never bakes a backend's name into its own config or state files; a backend's required filename is seeded as that backend's per-profile detail, keeping the core backend-neutral.

## What this ADR deliberately does NOT decide

The transcript mirror, verbosity, and retention (ADR-0008). The gate policy (ADR-0004); this ADR only places the per-backend hook declaration in the profile. The message model (ADR-0010), channels (ADR-0009), routing (ADR-0003), and the access model (ADR-0005). The actual adapter implementation for any backend, how a new backend registers at runtime, and the exact profile schema are left open.

## Consequences

pao's core stays backend-neutral: everything that differs between backends lives in the profile, so a new backend is a profile plus an adapter, not a change to the core. The per-backend specifics deferred by earlier ADRs (the transcript format, the gate hook, the tool interface) now have one home. The contract test keeps a backend upgrade from breaking the mirror unnoticed, and generic naming keeps the core free of any single backend's assumptions.
