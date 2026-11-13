# ADR 0006: Temporary agents

**Status:** Proposed (2026-11-13)

## Context

Some work is one-off: a scoped task that wants its own agent for a while and then goes away. pao needs these to be real agents, not a lesser side-channel, and their teardown must be deliberate so their output is never lost by accident. ADR-0001 fixed agents and their I/O, ADR-0003 made every agent a routing target, and ADR-0005 fixed access. This ADR fixes temporary agents on top of those.

## Decision

1. **A temporary agent is a first-class agent with an ephemeral profile.** It reuses everything a normal agent has (the transcript mirror, the control plane, the message model, its access-matrix rows) and adds an isolated working directory, separate from any other agent's context. The operator talks to it directly through its own stream on a channel named at launch, and other agents reach it like any peer. It is not a harness-internal sub-agent.

2. **Spawning is: create the ephemeral profile, provision an isolated workdir, bind it to a channel, launch through the control plane.** The workdir is provisioned from a source, for example a git clone or pushed files. Once launched it inherits streaming, routing, pause/resume and agent-to-agent calls for free.

3. **Teardown is an explicit kill, never an automatic reap.** pao tracks every temporary agent and marks an idle or unused one with a visible status flag, but does not kill it on a timer. A kill happens only when the operator or an agent asks for one, and a kill requested by an agent is itself an action, subject to the access matrix (ADR-0005) and the approval gate (ADR-0004).

4. **A kill always harvests before it destroys.** Before teardown, pao writes the agent's outputs to a small shared store, readable by other agents in the scoped slices the access matrix (ADR-0005) allows, and surfaces them to whoever asked for the kill. Harvesting is not optional, so a killed agent's results are never lost to teardown.

5. **Temporary agents are spawned from recipes, which are data, not code.** A recipe bundles a profile, a workdir source and a tool set, so one command spawns a preconfigured temporary agent. Recipes are defined outside pao's code and can be pulled from a git repository; adding one never changes pao itself.

## What this ADR deliberately does NOT decide

The exact ephemeral-profile fields; the recipe file format and where recipe sets live; the harvest store's format and retention; the stalled/unused detection thresholds; and first-run provisioning details for a fresh workdir, are each deferred. The control-plane behaviors this ADR says a temporary agent inherits (lifecycle, pause/resume, liveness) are specified in their own later ADR, not here.

## Consequences

A one-off task gets a real, addressable agent with its own isolated space, driven and reached like any other, spun up in one command from a shareable recipe. Nothing a temporary agent made is lost to teardown, because a kill harvests first. The lifecycle is deliberate: agents are marked, not silently reaped, and removed only on an explicit, output-preserving kill.
