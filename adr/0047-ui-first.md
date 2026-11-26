# ADR 0047: The UI is the primary surface for the setup-and-launch flow

**Status:** Proposed (2026-11-26)

Extends ADR-0005 (the management surface: a web app plus an HTTP API, private and localhost by default), and builds on ADR-0039 (the UI is a core piece pao owns, not a driver), ADR-0001 (pao is driven from chat), ADR-0046 (binding an agent to a channel, the steering grant), ADR-0015 (profiles), ADR-0040 (the per-user orchestrator), ADR-0041 (task-agents from templates), ADR-0042 (the data source a task runs on), ADR-0012 (the access matrix), ADR-0004 (the gate) and ADR-0019 (subject attribution).

## Context

ADR-0005 point 3 already gives pao a management surface, a web app plus an HTTP API, and frames it as the configuration and control surface, with chat (ADR-0001) as how pao is driven day to day; ADR-0005 point 4 keeps that surface private and bound to localhost by default. ADR-0039 names the UI among the core pieces pao owns and refines in the ADRs that follow it.

In the redesign, the missing UI is named as the main reason v1 stalled: there was no primary surface where a user could define a channel, provision the material a task runs on, pick a profile, launch an agent and bind it to a channel, so every such setup step had to be done at the command line or coaxed through an agent. This ADR raises that web-app surface from a configuration back-office to the **primary surface for the setup-and-launch flow**, reachable directly, without changing ADR-0005 point 3's separation of setup from the day-to-day driving of a running agent, which stays chat.

## Decision

1. **The UI is pao's own primary surface for setting up and launching what agents run on.** It is the web app of ADR-0005 point 3, and it is pao's own (ADR-0039 names the UI a core piece pao owns, not a driver). This ADR raises it from a back-office for the matrix and configuration to the **primary** surface a user reaches first, because the lack of one is what stalled v1. It does not change ADR-0005 point 3's line that the day-to-day **driving** of a running agent is chat (ADR-0001): the UI's primacy is over the setup-and-launch flow, not over conversing with an agent.

2. **The setup-and-launch flow is reachable directly from the UI, in one place:** register and define a **channel** (channels are registered first), provision a **data source or folder** for a task (ADR-0042), pick a **profile** (ADR-0015), **launch** an agent, the per-user orchestrator (ADR-0040) or a task-agent instanced from a template (ADR-0041), and **bind** an agent to a channel (the steering of ADR-0046). Each of these is a step a user can take from the UI itself.

3. **The UI talks to pao, not to an agent; no agent mediates a setup action.** A UI action is a call to pao's own management operations (the HTTP API of ADR-0005 point 3), not a message to an agent, and the UI is not a channel with a default agent. This is what "reachable directly" means: defining, provisioning, launching and binding do not route through an agent, which is the indirection the redesign set out to remove.

4. **The UI is another front on the same operations, checks and gate, not a new authority.** Every UI action maps to a pao operation that also exists at the other surfaces, the node command line and the chat commands of ADR-0046, and runs through the same checks: the access matrix (ADR-0012) and the gate (ADR-0004), measured against the acting subject's own grants. A UI session's actor is a **subject**, attributed by the UI's own authentication as a channel attributes a sender (ADR-0019). So the UI adds a way in and no new authority, the stance ADR-0046 took for chat. Administration, grants, subjects and the operator flag, may be done from the UI and is still gated, as ADR-0005 point 3 already places it on the management surface.

5. **The UI stays private and access-gated, localhost by default** (ADR-0005 point 4). It is not exposed automatically; exposing it beyond localhost is the operator's own choice, unchanged by this ADR.

6. **The UI is primary but not exclusive.** Chat (ADR-0046) and the node command line remain, and this ADR retires neither; the same operation can be taken from any of the three fronts, with the UI the one a user reaches first for setup. Answers and effects are the same whichever front a command comes from, as ADR-0046 point 2 already holds for chat against the command line.

## What this ADR deliberately does NOT decide

- The UI's framework and implementation technology.
- Its authentication mechanism and exactly how a UI actor maps to a subject (ADR-0019), left with the identity work.
- Which operations are in the first build, and the order they are added.
- Whether the UI also streams a running agent's output and view (the stdout and backtrack side of ADR-0038 and ADR-0008) or manages setup only, and if it does, whether that view is live or polled.
- Whether a UI action that needs an approval answers the gate in the UI itself or elsewhere, and how multiple concurrent UI sessions are handled.

## Consequences

The surface whose absence stalled v1 becomes the primary one: a user defines a channel, provisions a source, picks a profile, launches an agent and binds it to a channel from the UI directly, without the command line or an intermediary agent. Because the UI is a front on operations that already exist, it adds reach rather than authority: the access matrix (ADR-0012) and the gate (ADR-0004) bound a UI action exactly as they bound the same action from chat or the command line, and the UI actor is a subject like any other (ADR-0019). The day-to-day driving of a running agent stays chat (ADR-0001, ADR-0005 point 3), so this ADR elevates the setup surface without disturbing that split. This **builds on ADR-0005** (whose web-app management surface it raises to primary) and **ADR-0039** (the UI as a core piece pao owns), and on **ADR-0001, ADR-0046, ADR-0015, ADR-0040, ADR-0041, ADR-0042, ADR-0012, ADR-0004 and ADR-0019**; it supersedes nothing, extending ADR-0005 point 3's framing within what that ADR already allows.
