# ADR 0059: An agent reads a message's sender only as the node or its profile sets

**Status:** Accepted (2026-11-28)

Supersedes ADR-0054. It keeps ADR-0054 in full except the last sentence of point 4, which it carries from ADR-0001 and which this ADR replaces. Builds on ADR-0058 point 10 (the sender prefix, off by default) and point 4 (the agent treats its channel as an opaque terminal).

## Context

ADR-0001 point 4, kept unchanged by ADR-0054, says the agent "receives `[sender]: text` and never knows the channel or transport". Every message an agent reads has carried its sender's name since.

ADR-0058 point 10 makes the sender a setting: off by default, the sender's name, or the sender and the channel. With one owner per channel, the sender is not in doubt, and the prefix matters only once agents talk to each other. ADR-0058 did not supersede ADR-0054, so the two disagree on what an agent reads by default. The index makes any change to an accepted ADR a full supersession. This is that ADR.

## Decision

Points 0 to 3 and point 5 are ADR-0054's, unchanged. Point 4 keeps its first two sentences; its last sentence is replaced:

4. **Two configs.** An **orchestrator config** (sources, channels, routing, credentials) and a per-agent **profile** (agent-intrinsic only). **The agent is source-agnostic: it reads a message's text, with the sender's name, and the channel's, before it only as ADR-0058 point 10's setting says, and never knows the transport or the platform.**

- By default the agent reads the text alone.
- The setting is the node's, and an agent's profile may override it.
- The channel an agent may be told is the channel's name, the one every other part of pao addresses it by (ADR-0058 point 2), never the provider, the platform or the conversation's id.

## Consequences

- An agent that relied on `[sender]: ` before every message reads the bare text after upgrading, unless the node or its profile sets the prefix to the sender's name.
- Nothing else of ADR-0001 or ADR-0054 changes.
