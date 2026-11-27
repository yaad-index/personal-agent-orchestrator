# ADR 0050: Agent Markdown, rendered natively by each channel

**Status:** Accepted

Extends ADR-0010 (the message model) and ADR-0019 (channel adapters). Builds on ADR-0008 (the mirror and its levels), ADR-0009 (the communication layer), ADR-0027 (action buttons) and ADR-0038 (topics as conversations).

## Context

ADR-0010 fixed one message shape and gave it text, but it did not say what format that text is in. In practice an agent writes Markdown, because that is what a coding-agent harness produces. Every channel passes the text on as it is. A channel that renders Markdown shows it well, but a chat platform that has its own formatting shows the raw markers: asterisks, backticks and fences in the middle of the reply. Agent output is then hard to read on exactly the channels a human uses most.

Not all text pao sends is the agent's. pao writes its own text too: approval questions that quote a tool's input as raw JSON, command replies, notices and the prefix of a standing binding. At higher mirror levels it also writes tool calls, tool results and reasoning as rendered transcript entries (ADR-0008). If any of that were read as Markdown, a `*` or a backtick in a JSON argument would turn into formatting and the text would be shown wrong. The message kind of ADR-0010 point 1 does not draw this line: a rendered tool call goes out as content, the same kind as the agent's own words.

## Decision

1. **A message says what format its text is in: Markdown or plain.** The format travels with the message to every channel, and across nodes on the peer path, so the adapter that shows it knows how to read it.

2. **Only an agent's own words are Markdown.** That covers the conversation entries of its transcript (ADR-0008) and the text an agent sends through its tool interface to pao. Everything else is plain and is never parsed:
   - what pao writes itself: system events, approval questions, command replies, notices and a binding's prefix;
   - what a human writes;
   - mirrored tool calls, tool results, reasoning and other transcript entries.

3. **Each channel renders Markdown in its own native form.** The adapter, not the agent and not the communication layer, translates. An agent never needs to know where its words are shown. A channel with no rich formatting shows clean plain text, with the markup removed and never shown as raw markers. A channel may show a plain text that is code-like, such as a mirrored tool call, in its code style. That is a matter of style; the text is never parsed.

4. **The stored text is the text the agent wrote.** The message store, what another agent reads as `[sender]: text` (ADR-0010 point 1) and every quote an agent makes keep the Markdown. Only what a channel shows is rendered.

5. **On a channel that marks formatting as ranges over plain text, the adapter sends the ranges.** Some platforms accept either an inline markup language, with its own escaping rules, or the plain text plus a list of formatted ranges. Where both exist, the adapter sends the ranges. Nothing then needs escaping, and a message can't fail to send because of a markup error. When a long message is split at the platform's size limit, a range that crosses a cut is closed at the cut and reopened after it. If the platform still refuses a rendered message, the adapter sends the same text as plain text, so the message is never lost to its formatting. Buttons (ADR-0027) and topics (ADR-0038) are unchanged.

## What this ADR deliberately does NOT decide

- The exact Markdown dialect beyond CommonMark, and how each construct a platform lacks is shown on that platform (for example a table or a heading).
- Rendering on any channel other than the first one built. Each adapter adopts point 3 when it is next worked on, and until then shows the text as it does today.
- Whether a human's own message may opt into Markdown.
- Whether a rendered transcript entry should carry Markdown of its own, such as a tool result that is itself Markdown.

## Consequences

Agent output reads as formatted text on a chat platform, and each new channel adds only its own renderer. pao's own text is never at risk of being mis-rendered, because the parser never sees it.

Points 1 and 2 add a field to the message and to the peer-path message, and the places that make a message set it. A message that carries no format is plain, so a message from a node that predates this ADR shows as it does today.

A human who quotes part of a rendered message quotes what they saw: the rendered text without its markers. A quote is matched against the stored text (ADR-0010 point 3), which is Markdown, so the two can differ. Wherever pao resolves a human's quoted snippet, it compares against the rendered plain form of each candidate, not against the stored Markdown.
