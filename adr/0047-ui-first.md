# ADR 0047: The UI is the primary surface for setting up channels, sources and profiles

**Status:** Proposed (2026-11-26)

Extends ADR-0005 (the management surface: a web app plus an HTTP API, private and localhost by default), and builds on ADR-0039 (the UI is a core piece pao owns, not a driver), ADR-0001 (pao is driven from chat), ADR-0046 (operator commands and channel steering), ADR-0015 (profiles), ADR-0042 (the data source a task runs on), ADR-0012 (the access matrix), ADR-0022 (the operator flag), ADR-0004 (the gate) and ADR-0019 (subject attribution).

## Context

ADR-0005 point 3 already gives pao a management surface, a web app plus an HTTP API, and frames it as the configuration and control surface, with chat (ADR-0001) as how pao is driven day to day; ADR-0005 point 4 keeps that surface private and bound to localhost by default. ADR-0039 names the UI among the core pieces pao owns and refines in the ADRs that follow it.

In the redesign, the missing UI is named as the main reason v1 stalled: there was no primary surface where a user could set up what agents run on. The redesign wants the UI to carry the whole flow through to launching and binding an agent. Two of those steps, **launching** an agent and **binding** it to a channel, are what ADR-0001 point 5 reserves to chat ("the user drives pao mostly from chat, spawning agents, routing, not a console or dashboard; the management UI is for configuration/admin only"), reaffirmed by ADR-0040 point 4 and ADR-0041 point 4 (no new spawn path). Making the UI primary for those would change ADR-0001 point 5, which the index's all-or-nothing rule turns into a full supersession of the founding ADR. On the operator's call, that is split off: **this ADR takes only the configuration setup flow, which ADR-0005 point 3 already places on the management surface, so it supersedes nothing; launching and binding from the UI are left to a following ADR that supersedes ADR-0001.**

## Decision

1. **The UI is pao's own primary surface for the configuration setup flow.** It is the web app of ADR-0005 point 3, and it is pao's own (ADR-0039 names the UI a core piece pao owns, not a driver). This ADR raises it from a back-office to the **primary** surface a user reaches first for setting up what agents run on, because the lack of one is what stalled v1. It does not change ADR-0005 point 3's line, nor ADR-0001 point 5's, that pao is driven from chat: the UI's primacy here is over configuration, not over spawning or driving an agent.

2. **The configuration setup flow is reachable directly from the UI, in one place:** register and define a **channel** (channels are registered first), provision a **data source or folder** for a task, which the orchestrator itself provisions (ADR-0042 point 1), and pick a **profile** (ADR-0015). These are the configuration steps ADR-0005 point 3 already assigns to the management surface; **launching** an agent and **binding** it to a channel are not among them here (point 7).

3. **The UI talks to pao, not to an agent; no agent mediates a setup action.** A UI action is a call to pao's own management operations (the HTTP API of ADR-0005 point 3), not a message to an agent, and the UI is not a channel with a default agent. This is what "reachable directly" means: defining a channel, provisioning a source and picking a profile do not route through an agent, which is the indirection the redesign set out to remove.

4. **The UI is a front on pao's management operations, not a new authority, and it is not the same front as chat.** Every UI action maps to a management operation that also exists at the node command line, and runs through the same checks: the access matrix (ADR-0012) and the gate (ADR-0004), measured against the acting subject's own grants. A UI session's actor is a **subject**, attributed by the UI's own authentication as a channel attributes a sender (ADR-0019). The UI is **not** a third copy of chat's command set: administration, defining channels and configuration are kept off chat by ADR-0046 point 5, and remain so; the UI and the command line are where they are done. That administration is **operator-only** (ADR-0005 point 1, ADR-0012 point 2's default-deny), and the **operator flag is set only at the operator's local surface** (ADR-0022 point 2), never from a UI that may be exposed beyond localhost. A setup action that raises an approval question is answered by the operator only (ADR-0046 point 4, ADR-0038 point 7). So the UI adds reach and no new authority, the stance ADR-0046 took for chat.

5. **The UI stays private and access-gated, localhost by default** (ADR-0005 point 4). It is not exposed automatically; exposing it beyond localhost is the operator's own choice, unchanged by this ADR, and is why the operator flag in point 4 is never set from it.

6. **The UI is primary but not exclusive.** The node command line remains and this ADR retires nothing; the same configuration operation can be taken from the UI or the command line, with the UI the one a user reaches first. Its effect lasts exactly as the same operation from the command line (ADR-0046 point 9), and answers read the same from either (ADR-0046 point 2).

7. **Launching and binding from the UI are deferred to a following ADR.** Making the UI primary to **launch** an agent (agent lifecycle, operator-only under ADR-0046 point 3, with no new spawn path per ADR-0040 point 4 and ADR-0041 point 4) and to **bind** an agent to a channel (channel-scoped steering under ADR-0046 point 3, the operator by role or a channel owner) both change ADR-0001 point 5's placement of spawning and routing on chat. The index requires that as a full supersession of ADR-0001, so it is a following ADR's subject, not decided here. Until then, launch and bind stay where ADR-0046 and the command line put them.

## What this ADR deliberately does NOT decide

- Launching and binding from the UI, which change ADR-0001 point 5 and are a following ADR's subject (point 7).
- The UI's framework and implementation technology.
- Its authentication mechanism and exactly how a UI actor maps to a subject (ADR-0019), left with the identity work.
- Which setup operations are in the first build, and the order they are added.
- Whether the UI also streams a running agent's output and view (the stdout and backtrack side of ADR-0038 and ADR-0008), which is a view concern separate from this setup surface.

## Consequences

The configuration half of the surface whose absence stalled v1 becomes the primary one: a user defines a channel, provisions a source and picks a profile from the UI directly, without the command line or an intermediary agent. Because the UI is a front on operations that already exist on the management surface (ADR-0005 point 3), it adds reach rather than authority: the access matrix (ADR-0012) and the gate (ADR-0004) bound a UI action exactly as they bound the same action from the command line, administration is operator-only and the operator flag stays local (ADR-0022 point 2), and the UI actor is a subject like any other (ADR-0019). It is not a third copy of chat, since administration and configuration are off chat (ADR-0046 point 5). Spawning and driving an agent stay on chat (ADR-0001 point 5), so this ADR elevates the configuration surface without disturbing that split; the UI's own launch and bind are left to the ADR that supersedes ADR-0001 (point 7). This **builds on ADR-0005** (whose web-app management surface it raises to primary) and **ADR-0039** (the UI as a core piece pao owns), and on **ADR-0001, ADR-0046, ADR-0015, ADR-0042, ADR-0012, ADR-0022, ADR-0004 and ADR-0019**; it supersedes nothing, extending ADR-0005 point 3's framing within what that ADR already allows.
