# ADR 0032: The governor's questions and notices go to its requester

**Status:** Accepted (2026-11-21)

Supersedes ADR-0031 (the governor, shared per node and started on demand). Keeps its points 1 to 3 and 5 to 8, its deferred list and its consequences unchanged. Changes point 4, which now also covers the governor's stderr. Extends ADR-0029 (output routing).

## Context

ADR-0031 point 4 sends everything the governor says to the requester alone, privately. It speaks of the governor's own output. It says nothing of the governor's stderr: the gate's questions about its tool calls, and pao's notices about it.

Those follow ADR-0029 point 4: an agent's stderr goes to its stderr destinations, by default the private conversation of every operator bound on a channel. The governor has no bindings (ADR-0031 point 4), so the default always applies to it. A question or a notice about the governor therefore reaches every operator, not the one it is serving.

The first test of a governor on a node showed it. The governor's first start stopped at a prompt of its backend, and pao's notice that it was not ready went to the operator's private conversation. That notice carries the agent's terminal pane, and a governor's pane can show the request it is serving. On a node with several operators, one operator's request would be shown to all of them. Point 4 exists to prevent exactly that.

A stderr binding on the governor would not fix it. A binding is a destination an operator sets once. Nothing makes it the operator the governor is serving at the moment, so it would bring back the very exposure point 4 rules out.

## Decision

4. **It is invoked as `/gpao <text>`, and everything about it goes to the requester.**
   - `/gpao` is the default. A node's configuration may name another prefix in its place, as long as it is not pao's command prefix. Below, `/gpao` stands for whichever prefix the node uses.
   - Like `/pao` (ADR-0026 point 1), `/gpao` is pao's and never reaches an ordinary agent. The text after it goes to the governor alone, as a request from the sender.
   - `/gpao` is never another spelling of a `/pao` command. A command typed after `/gpao` is a request to translate, not a command.
   - A reply to the governor's clarifying question is sent with `/gpao` too. Plain text keeps its ADR-0023 routing.
   - Only operators may use it. With no governor configured on the node, the sender is told so.
   - Everything the governor says goes to the requester alone, privately, as an ADR-0026 point 5 answer does. That covers proposals, clarifying questions and refusals. This holds even when the request was sent in a group. The governor has no standing bindings (ADR-0029 point 3).
   - **New: the governor's stderr goes to the requester too.** A gate question about the governor's tool call, and pao's notice about the governor, go to the subject it is serving, privately, on the channel the request came from. An example of such a notice is that it did not become ready. They never go to every operator, as ADR-0029 point 4's default would send them.
     - The requester may answer such a question, since only an operator's request reaches the governor (ADR-0026 point 3).
     - A question still passes ADR-0029 point 4's rule that it reaches only those who may answer it, and every copy is still closed once it is settled.
     - The governor still takes no stderr binding.
   - **With no request in hand, no one is told.** Before its first request, or in a turn another agent started, there is no requester. A question that cannot be put is not asked, and the action it was for is refused. A notice is logged and sent to no one.

## Consequences

- One operator's request, and anything about it, now reaches no other operator by any path: not by the governor's output, its questions, or pao's notices about it.
- The operator a question goes to is the one who asked for the work it is about, so the person who can best judge it is the one asked.
- A governor that is not serving anyone cannot ask, so it cannot act on a tool call that needs a question. It has nothing to do outside a request anyway.
- ADR-0031 is superseded in full. Its points 1 to 3 and 5 to 8, its deferred list and its consequences carry over unchanged. Only point 4 is replaced, with the same bullets and the two new ones.
