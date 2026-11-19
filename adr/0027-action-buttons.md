# ADR 0027: Action buttons on a gate question

**Status:** Accepted (2026-11-19)

Extends ADR-0004 (the approval gate), ADR-0009 (channels), ADR-0010 (message model), ADR-0019 (channel adapters and sender identity), ADR-0026 (operator commands from chat).

## Context

The gate's question to the operator (ADR-0004) is sent to the channels as text. It names the agent and the action, and lists the typed answers: `/pao allow <id>`, `/pao always <id>` and `/pao deny <id> [reason]`. Since ADR-0026 the operator can type one of these on a chat channel. Answering means copying an id out of the question and typing a command, often on a phone, within the gate's ask timeout. An unanswered question is a denial.

ADR-0026 point 7 settled what a button is: a shortcut for a typed command, never a separate path. A press is handled as that command from the person who pressed it, and it is checked again when it arrives. It left the layout per platform open, and it did not say where the platform-specific work lives.

That work differs by platform. Each platform has its own way to attach buttons to a message, its own limits on what a button can carry, its own event for a press, and its own way to change a message after it is sent. Done without a boundary, these details would spread into the communication layer, the gate and the daemon, and a second platform would have to be fitted around the first.

## Decision

1. **A message pao sends may carry actions, in a platform-neutral form.** An action is a short label and the typed command it stands for (ADR-0026 point 7). The message model gains this list, and nothing in it is platform-specific. A gate question carries three actions: allow, always and deny, each with its command and the question's id. The deny button carries no reason; a deny with a reason is the typed command. The question's text still lists the typed answers, so a channel that shows no buttons loses nothing, and every action stays reachable by typing.

2. **Interactive control is a capability interface that the core consumes. All platform code stays in that platform's adapter.** The communication layer, the gate and the daemon know only the platform-neutral interface:
   - **Show:** a channel that can show actions renders a message's actions as buttons. A channel that cannot show them ignores the actions and shows the text.
   - **Press:** a press reaches the layer as an ordinary inbound command. It carries the command's text, the platform's authenticated sender of the press (ADR-0019 point 2), and a mark that it came from a press, so the answer can go back in the platform's own way.
   - **Close:** when what a set of actions answers is settled, the layer tells each channel that showed them to close them, with a one-line outcome.

   The platform's own work stays inside its adapter package:
   - how buttons are drawn and what a press carries;
   - the platform's size limits;
   - acknowledging a press;
   - editing a message after it is sent.

   No package outside the adapter imports a platform library or names a platform type. The capability is optional, like the other per-adapter capabilities: an adapter that does not implement it is a channel without buttons.

3. **The first adapter is the bot adapter. Any later platform is its own adapter, written to its own platform's API.** This ADR scopes the first build to the existing bot adapter and to the interface of point 2. A later platform implements the same interface in its own package, the way its platform provides presses and message edits. It shares no code with the first adapter beyond the interface. If a platform cannot meet the interface as written, that is decided in its own ADR, not by bending the first adapter.

4. **A press carries the command it stands for, and it is checked as that command.** A button's payload is the typed command itself, not a key into a table that pao keeps. A forged or replayed payload is then exactly a message the presser could have typed. It is attributed to the presser's subject through the identity binding (ADR-0019 point 3) and checked under ADR-0026 point 3 when it arrives. A press from a subject that is not the operator is answered as an unknown command. A press on a question that is already settled gets the typed command's own answer for a question that is not waiting. In both cases nothing is decided. If a command does not fit in the platform's payload limit, the adapter shows no button for that action, and the typed command remains.

5. **A press's answer goes to the presser alone.** As ADR-0026 point 5 requires for a typed command, the answer to a press goes only to the subject who pressed. Where the platform has a private acknowledgement of a press, that acknowledgement is the answer. Otherwise the answer is a reply on the channel the press came from. The answer never fans out to other channels.

6. **A settled question's buttons are closed on every channel that showed them.** A gate question can be shown on more than one channel. When it is answered on any of them, or its ask timeout passes, the layer asks each channel that showed buttons to remove them and to show the outcome: allowed, allowed and remembered, denied, or expired. Closing is best effort. A copy whose buttons could not be removed still fails closed: a press on it gets the answer for a question that is not waiting, and decides nothing.

## What this ADR deliberately does NOT decide

- The button labels and their layout. The first build uses the three answers in the order the question lists them.
- Buttons for any other action. ADR-0026 point 7 names pause and resume on a bound agent's output as an example. Each further set of actions is a later change on the same interface.
- Whether a press can carry a deny reason, and how.
- A confirmation step before `always` writes a rule into the gate's policy.
- Any platform beyond the first adapter.
- Recording presses in an audit log. That waits for the audit-log ADR, as ADR-0026 says for typed commands.

## Consequences

The operator answers a gate question with one press, on the device where they read it, within the ask timeout. The question's id never has to be copied. A press adds no authority: it is a typed command under the same checks, so a forged payload gains nothing that typing would not give the same person. A second platform is added by writing one more adapter against the interface, without touching the communication layer, the gate or the first adapter. It also cannot reuse the first adapter's code, so the platform work is repeated for each platform.

The message model now carries actions. Every channel receives them, and a channel without buttons ignores them. The layer must remember which channel showed which actions until they are settled. A press on `always` writes a rule into the gate's policy, exactly as the typed command does, so a mistaken press is undone at the operator surface, not from chat. Because closing is best effort, a stale button can remain visible. It is harmless, since a press on it decides nothing.
