# ADR 0010: The message model

**Status:** Proposed (2026-11-13)

## Context

ADR-0009 fixed the channel layer and that every inbound source is normalized to a sender-tagged message. This ADR fixes that message itself: the shape pao carries across all sources, how a message is targeted for reply, reaction, or fetch, and how a choice is offered to the human.

## Decision

1. **One message shape across all sources.** A message carries an id, a timestamp, its sender, a kind, its content (text, with a voice note already turned to text in the channel layer), normalized attachments, an optional reply target, and its reactions. The agent still sees only `[sender]: text` (ADR-0009); the rest is orchestrator-side.

2. **Ids are the primary handle; a quoted snippet is the fallback.** Reply, reaction, fetch, and edit address a message by id. pao keeps a rolling map of recent ids over a configurable window. When an id is ambiguous or has fallen out of the window, pao quotes part of the message back instead, and the quoted snippet disambiguates. The full history still lives in the transcript (ADR-0008) past the id window.

3. **Id-first runs in both directions.** A reference or reaction from the human arrives id-first with the same quote fallback, not only pao's outbound. Because an agent has no direct access to the underlying platform (ADR-0009), to act on a message it does not already hold it calls a fetch-by-id tool on its own interface rather than reaching the platform itself.

4. **A reaction targets a message id; it is not a free-standing message.** A reaction is who reacted, with which emoji, on which id. On a chat platform it renders as that platform's native reaction; on the peer path, which has no native reactions, it is delivered as a small message so the target still sees that a reaction was made.

5. **A choice is offered as an action button, with reactions as a lightweight extra.** When pao offers the human a choice, the primary mechanism is an action button on platforms that support one, because it is unambiguous and carries its own payload. Reactions are kept as a cheap, optional signal, deliberately not the load-bearing way to make a choice.

## What this ADR deliberately does NOT decide

The channel layer and source normalization (ADR-0009). The routing of a message to an agent (ADR-0003 set the direction; the algorithm stays deferred). The outbound policy that decides when and whether a message is sent (quiet hours, rate-limiting, coalescing, budget) is the budget-and-outbound-notifications ADR. The exact id-window size, the attachment normalization format, the button and reaction taxonomy, and each platform's rendering specifics are left open.

## Consequences

Every source speaks one message shape, so the agent interface and the channel adapters share one model and a new source needs no new message type. Addressing by id with a quote fallback keeps references stable without an unbounded id memory, and the transcript remains the full record behind the window. Making a reaction target an id, and leaning on buttons for choices, keeps a choice unambiguous and payload-carrying, while reactions stay available as a light signal.
