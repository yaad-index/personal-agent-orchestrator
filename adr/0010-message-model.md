# ADR 0010: The message model

**Status:** Accepted (2026-11-13)

## Context

ADR-0009 fixed the communication layer and that every inbound source is normalized to a sender-tagged message. This ADR fixes that message itself: the shape pao carries across all sources, how a message is targeted for reply, reaction, or fetch, and how a choice is offered to the human.

## Decision

1. **One message shape across all sources.** A message carries an id, a timestamp, its sender, a kind marking it as ordinary content or a system event, its content (text, with a voice note already turned to text in the communication layer), normalized attachments, an optional reply target, and its reactions. The agent reads the message as `[sender]: text`, with source and transport hidden (ADR-0001); it targets a message not from that readable line but by quoting it, or by id through a tool call on its interface to pao (points 2 and 3).

2. **Ids are pao's primary handle; a quoted snippet is the fallback.** Within pao, reply, reaction, and fetch address a message by id, kept in a rolling map of recent ids over a configurable window. When an id is ambiguous or has fallen out of the window, a quoted snippet of the message disambiguates instead.

3. **The agent's handle is the quote; pao resolves it to an id.** An agent reads a clean `[sender]: text` line with no id (ADR-0001), so it targets a message by quoting the text it was shown, and pao resolves that quote to the id in its map. Replying to the current message needs neither: the agent just answers and pao attributes the reply by recency. The raw id reaches the agent only when it fetches a message through a tool call on its interface to pao, since channels run only between the human and pao, never between pao and an agent (ADR-0001), so the agent has no direct access to the platform. This matching runs in both directions: a reference or reaction from the human is resolved the same way. That a backend provides such a tool interface is a requirement handed to the pluggable-backends ADR.

4. **A reaction targets a message id; it is not a free-standing message.** A reaction is who reacted, with which emoji, on which id. On a chat platform it renders as that platform's native reaction; on the peer path, which has no native reactions, it is delivered as a small message so the target still sees that a reaction was made.

5. **A choice is offered as an action button, with reactions as a lightweight extra.** When pao offers the human a choice, the primary mechanism is an action button on platforms that support one, because it is unambiguous and carries its own payload. Reactions are kept as a cheap, optional signal, deliberately not the load-bearing way to make a choice.

## What this ADR deliberately does NOT decide

The communication layer and source normalization (ADR-0009). The routing of a message to an agent (ADR-0003 set the direction; the algorithm stays deferred). The agent's tool interface to pao as a backend requirement is the pluggable-backends ADR. The outbound policy that decides when and whether a message is sent (quiet hours, rate-limiting, coalescing, budget) is the budget-and-outbound-notifications ADR. The exact id-window size, the attachment normalization format, and the button and reaction taxonomy are left open.

## Consequences

Every source speaks one message shape, so the agent interface and the channel adapters share one model and a new source needs no new message type. Addressing by id with a quote fallback keeps references stable without an unbounded id memory, and the quoted snippet carries a reference that has aged out of the window. Making a reaction target an id, and leaning on buttons for choices, keeps a choice unambiguous and payload-carrying, while reactions stay available as a light signal.
