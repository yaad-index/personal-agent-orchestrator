# ADR 0045: A channel's membership and ownership are separate grants, and inbound is preprocessed by type

**Status:** Proposed (2026-11-25)

## Context

The channel model is already rich: a channel is a communication endpoint with a per-source adapter (ADR-0009, ADR-0019), every inbound carries an authenticated per-sender id resolved to a subject (ADR-0019), a channel is a resource in the access matrix any granted subject may use for stdin and stdout (ADR-0023, superseded by ADR-0038, point 8), and commands from chat are addressed with `/pao` and are the operator's (ADR-0026).

Two things the redesign sharpens are not yet fixed: that being in a channel and being able to command it are different grants, and how a non-text inbound, a file or a voice clip, reaches an agent that reads normalized messages.

## Decision

1. **A channel is a communication endpoint; comms and access are separate.** A channel is bound to a source and an id (ADR-0009, ADR-0019), and pao is multi-source, each platform its own adapter. That a channel is a place messages flow is one thing; what a subject may do there is access, decided by the matrix (ADR-0005, ADR-0012), and the two are kept apart.

2. **Membership grants read; ownership grants command.** A channel resource carries two distinct grants. **Membership** lets a subject read the channel's conversation, see what is said and receive output; this is the channel use of ADR-0038 point 8, with each inbound still attributed to its subject (ADR-0019). **Ownership** lets a subject issue the `/pao` commands that steer the channel or its agent (ADR-0026). A member who is not an owner reads but cannot command, and a command is honored only from an owner. This makes explicit, as two grants, what was one axis of channel use plus one axis of command authority, and it generalizes ADR-0026's operator-only commands to a channel's owners, the operator being an owner of every channel.

3. **An agent runs on one channel.** An agent's input is one channel, its home (ADR-0038 point 3's single stdin), and that channel's members read its conversation; its output still fans to its stdout channels (ADR-0038 point 2) unchanged. This does not narrow ADR-0038, it names the home channel a per-user agent runs on.

4. **Inbound is handled by type, preprocessed before the agent.** pao routes an inbound by its type. **Text**, and text carrying a command, reach the agent as the normalized message of ADR-0010, a command only when its sender is an owner (point 2). A **non-text** inbound, a file, a voice clip, an image, is first run through a per-type **preprocessor** pao applies before the agent: a voice clip becomes its transcript text, an image becomes a stored path or reference, and so on, each type its own preprocessor. The agent receives the preprocessor's normalized output (ADR-0010), never the raw upload, and an inbound of a type that has no preprocessor is refused, not passed through raw.

The internals left open: the preprocessor set and how a type maps to one, with new types added without a core change (as adapters are, ADR-0019 point 1); where a preprocessor's output, a transcript or a stored file, lives and under what access; how ownership is granted, and whether a channel may have several owners beyond the operator; and whether a preprocessor may itself be a driver of an ADR-0039 plane or stays pao-internal.

## Consequences

Being in a channel and being able to steer it are separate grants, so a channel can be shared for reading without handing everyone its command surface, which a single channel grant could not express. Per-type preprocessing means an agent always sees normalized text or a reference and never raw bytes, so a new input type is a new preprocessor rather than a change to every agent, and an unknown type fails closed. This **builds on ADR-0009 and ADR-0019** (the channel endpoint and sender identity), **ADR-0038** (the channel resource, the stdin and stdout streams, and the per-subject attribution they rest on), **ADR-0026** (operator commands, generalized to ownership), **ADR-0010** (the normalized message the agent reads), and **ADR-0005 and ADR-0012** (the two grants); it refines ADR-0038 point 8 and ADR-0026 rather than superseding them, since the channel machinery and the operator's command authority are kept and only separated into their two grants.

The cost is two grants where there was one notion of channel use, so the matrix now distinguishes a member from an owner and a mis-grant could hand the command surface too widely, which the gate (ADR-0004) still checks per command; and a preprocessor per input type to build and maintain, with the no-preprocessor-refuses rule as the safe default.
