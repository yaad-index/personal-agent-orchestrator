# ADR 0011: Pluggable backends

**Status:** Proposed (2026-11-13)

## Context

ADR-0001 fixed that agent I/O is terminal-in, transcript-out, and that a per-agent profile holds the agent-intrinsic config. Several later ADRs then deferred a per-backend specific here: the transcript path and format the mirror reads (ADR-0008), the per-backend gate hook (ADR-0004), the control-plane startup procedure and liveness source (ADR-0007), and the agent's tool interface (ADR-0010). This ADR fixes what makes a backend pluggable: the uniform contract every backend meets, and what its profile declares.

## Decision

1. **Every backend meets one uniform I/O contract.** A backend is any CLI coding agent that reads its terminal and writes a transcript (ADR-0001). pao drives them all the same way: paste in, read the transcript out. Claude Code is the first backend; others are added behind the same contract, not by changing pao's core.

2. **The agent profile declares every per-backend specific.** The profile (ADR-0001's per-agent config) carries, per backend: the input method pao uses to submit a paste; the transcript path and format the ADR-0008 mirror reads; the hook classes the backend offers and what each is for (the gate hook of ADR-0004 among them); the tool interface the agent exposes (the fetch-by-id tool of ADR-0010; the agent otherwise targets a message by quote, which pao resolves); the source for context, usage, liveness, and login expiry; and the backend's own commands (such as compact or reset). The core reads these from the profile and stays backend-neutral; the only other per-backend part is a small reader for that backend's transcript format.

3. **The transcript format is guarded by the contract test.** The per-backend transcript format the profile declares is pinned by the contract test of ADR-0008, which runs the real backend CLI and fails loudly if the format changes, so a backend upgrade cannot silently break the mirror.

4. **Fresh-agent startup is a per-backend procedure with a readiness timeout that prints the pane to the operator.** Bringing a backend to a ready prompt is a procedure declared in its profile, with a timeout that prints the terminal pane to the operator if it stalls (ADR-0007), rather than enumerating every first-run modal. Any file a backend requires on first run is seeded as that backend's per-profile detail.

5. **pao's own files use generic, pao-namespaced names.** pao never bakes a backend's name into its own config or state files; a backend's required filename is seeded as that backend's per-profile detail, keeping the core backend-neutral.

## What this ADR deliberately does NOT decide

The transcript mirror, verbosity, and retention (ADR-0008). The gate policy (ADR-0004); this ADR only places the per-backend hook declaration in the profile. The message model (ADR-0010), channels (ADR-0009), routing (ADR-0003), and the access model (ADR-0005). The actual per-backend implementation, how a new backend registers at runtime, and the exact profile schema are left open.

## Consequences

pao's core stays backend-neutral: what differs between backends is declared in the profile and read by a small per-backend transcript reader, so a new backend is its profile plus that reader, not a change to the core. The per-backend specifics deferred by earlier ADRs (the transcript format, the gate hook, the startup procedure, the tool interface) now have one home. The contract test keeps a backend upgrade from breaking the mirror unnoticed, and generic naming keeps the core free of any single backend's assumptions.
