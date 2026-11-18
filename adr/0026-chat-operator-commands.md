# ADR 0026: Operator commands from chat

**Status:** Proposed (2026-11-18)

Extends ADR-0005 (access and management surface), ADR-0009 (channels), ADR-0010 (message model), ADR-0022 (operator subjects), ADR-0023 (agent I/O and channel bindings).

## Context

pao is driven from chat (ADR-0001, ADR-0005 point 3), but most of what an operator does to steer agents exists only on the node's own command line. The command line can bind an agent's output to one channel and set its verbosity level. It can pause, resume and backtrack an agent's view, set a channel's default agent, and start, stop or restart an agent. The node's local chat client can also answer the gate's approval questions. None of these can be done from a channel on a chat platform. Such a channel recognizes one command, registration. Any other text is routed to an agent. So `/pao`, which ADR-0023 point 6 names as pao's command prefix, reaches the agent as input today.

An operator who is away from the node therefore cannot change what reaches them, or how much, from the place where they read it. An approval question sent to a platform channel cannot be answered on that channel. The answer has to be typed at the node.

## Decision

1. **`/pao` belongs to pao and never reaches an agent.** A message whose first word is `/pao` is a command to pao. The communication layer handles it before routing (ADR-0009 point 4). It is never delivered to an agent, whatever the channel, the routing precedence of ADR-0023 point 4, or the sender's sticky target. A `/pao` command that pao does not recognize is answered with usage. It is not passed on. Addressing an agent through `/pao agent <name> <message>` (ADR-0023 point 6) is one of these commands: pao reads it, then routes the message.

2. **A chat command is the operator's existing operation, not a new one.** Each command maps to an operation the node already exposes to its operator surface, such as the stream binding, the view controls, a channel's default agent, an agent's start, stop and restart, and the gate's answer. It runs through the same checks as when it is called from the command line. A command can do nothing from chat that the sender could not do at the operator surface. Answers and failures read the same either way.

3. **Who may run a command is decided per command, from the sender's subject.** The sender is attributed by the channel's identity binding (ADR-0019 point 3). Steering commands, which change a binding, a view, a default or an agent's state, and answering an approval question, are for a subject routed as the operator (ADR-0022). They are limited to the agents that subject reaches under the access matrix (ADR-0012). A subject that is not the operator may use `/pao help` and address the agents it may reach. Any other command from such a subject is refused as unknown. It is not refused as forbidden, so the refusal does not reveal what exists (ADR-0012 point 1).

4. **Administration stays at the operator surface.** Commands that change who may do what are not offered in chat. These cover grants, subjects and identity bindings, invitations, the operator flag (ADR-0022 point 2), channels and the node's configuration. They stay where ADR-0005 point 3 puts them, so a chat account that is taken over cannot widen its own access.

5. **A command's answer goes to the sender, on the channel it came from.** The reply to a command, and any refusal, is sent only to the subject who issued it, on the channel the command arrived on. It is never fanned out to the other channels an agent's output reaches. This follows the per-subject notice of ADR-0023 point 7. An agent's output that a command causes, such as a backtrack, goes where that agent's output is bound.

6. **`/pao help` is built from the command table and shows only what the sender may run.** Help lists each command with its usage and one line on what it does. A command's usage is shown when it is given wrong arguments. Help comes from the same table that pao dispatches commands from, so it cannot list a command that does not exist or miss one that does. It is filtered by point 3, so a subject is never shown a command it cannot run.

7. **Buttons are a shortcut for a typed command, never a separate path.** Where a channel's platform has action buttons, pao may attach them to its messages for common actions. ADR-0010 point 5 already prefers buttons for a choice. Examples are allow, deny and always on an approval question, and pause and resume on a bound agent's output. A button carries the command it stands for, and a press is handled exactly as that typed command from the person who pressed it. The press is attributed by the platform's authenticated sender of the press (ADR-0019 point 2), not by whom the message was sent to. It is checked again under point 3 when it arrives. A channel without buttons offers the same actions as typed commands only. Every button action has a typed command, so no action needs buttons.

8. **A command's effect lasts as long as the same change made at the operator surface.** A change made from chat is kept, or lost on restart, exactly as the same change made at the operator surface is. Whether a stream binding survives a node restart is a property of the binding, not of where it was set.

## What this ADR deliberately does NOT decide

- The exact command words and argument forms beyond `/pao agent <name> <message>`. These include the tag-addressing form that ADR-0023 point 6 deferred and a shorthand for it.
- Whether the existing `@name` and `#tag` prefixes stay as shorthands.
- Which operations are in the first build.
- Whether stream bindings and levels persist across a restart.
- The button layout per platform, and how a platform's own command menu lists `/pao`.
- Recording chat commands in an audit log, which waits for the audit-log ADR noted in the index.

## Consequences

An operator can steer agents and answer the gate from chat, where they already read the output. The node's command line becomes one front of several, not the only one. Text that starts with `/pao` can no longer reach an agent by accident. Because chat commands reuse the operator surface's operations and checks, chat adds a way in but no new authority: what a chat subject can do is what its subject can do at the operator surface, minus administration. Answers go only to the sender, so a command's output does not spread to channels other people read. Help and buttons come from the command table, so what is shown follows what exists. Each operation that is exposed in chat becomes part of what a stolen chat account can do as the operator. That is the reason administration stays off chat, and the reason the steering set is a deliberate list rather than everything the command line offers.
