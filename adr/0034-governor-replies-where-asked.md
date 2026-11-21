# ADR 0034: The governor replies where it was asked

**Status:** Accepted (2026-11-21)

**Superseded-by:** ADR-0035 (the governor knows what exists, never a secret), on its acceptance

Supersedes ADR-0032 (the governor's questions and notices go to its requester). Keeps its points 1 to 3 and 5 to 8, which ADR-0032 carried over from ADR-0031. Keeps point 4's rule for the governor's stderr, its deferred list, and its consequences except where noted below. Changes point 4's rule for the governor's output. This is the second change to point 4. The chain is ADR-0030, then 0031, then 0032, then this, each superseding the one before in full. Extends ADR-0029 (output routing).

## Context

ADR-0031 point 4, kept by ADR-0032, sends everything the governor says to the requester alone, privately, even when the request was sent in a group. That covers its proposals, clarifying questions and refusals.

The reason was caution: what the governor says carries one operator's request. But the governor only translates. It runs nothing, and a proposal's button runs the command as whoever presses it, under that person's own checks (ADR-0027 point 4). A proposal is the request put as a command, and the request was already said in the conversation it came from.

So the governor's output is like any agent's reply, and the ordinary rule fits it: a reply goes back to the conversation it was addressed from (ADR-0029 point 2). Asked in a group, the governor answers in the group, where the request was made. Asked privately, it answers privately.

The governor's stderr is different, and it does not change. pao's notice that the governor did not become ready carries its terminal pane, and a gate question shows its tool call. Neither is the request said back. ADR-0032 sends both to the requester alone, and that stays.

## Decision

4. **It is invoked as `/gpao <text>`. Its output goes back where it was asked, and its stderr to the requester alone.**
   - `/gpao` is the default. A node's configuration may name another prefix in its place, as long as it is not pao's command prefix. Below, `/gpao` stands for whichever prefix the node uses.
   - Like `/pao` (ADR-0026 point 1), `/gpao` is pao's and never reaches an ordinary agent. The text after it goes to the governor alone, as a request from the sender.
   - `/gpao` is never another spelling of a `/pao` command. A command typed after `/gpao` is a request to translate, not a command.
   - A reply to the governor's clarifying question is sent with `/gpao` too. Plain text keeps its ADR-0023 routing.
   - Only operators may use it. With no governor configured on the node, the sender is told so.
   - **Changed: the governor's output goes back to the conversation the request came from** (ADR-0029 point 2). That covers proposals, clarifying questions and refusals.
     - A request sent in a group is answered in the group. One sent privately is answered privately.
     - The output still passes the narrowest-reader rule (ADR-0029 point 6): it is shown only where every reader may reach the governor. A reply that fails it is held back and recorded, and never redirected elsewhere, as for any agent.
     - The governor still takes no standing binding and no channel binding (ADR-0029 point 3), so its output goes nowhere else.
     - A proposal's button in a group may be pressed by anyone the group admits. A press runs the command as the presser, checked against the presser's own access (ADR-0027 point 4).
   - **Kept: the governor's stderr goes to the requester alone.** A gate question about the governor's tool call, and pao's notice about the governor, go to the subject it is serving, privately, on the channel the request came from. An example of such a notice is that it did not become ready. They never go to every operator, as ADR-0029 point 4's default would send them, and never to the group the request came from.
     - The requester may answer such a question, since only an operator's request reaches the governor (ADR-0026 point 3).
     - A question still passes ADR-0029 point 4's rule that it reaches only those who may answer it, and every copy is still closed once it is settled.
     - The governor still takes no stderr binding.
   - **Kept: with no request in hand, no one is told.** Before its first request, or in a turn another agent started, there is no requester. A question that cannot be put is not asked, and the action it was for is refused. A notice is logged and sent to no one.

## How the reader rule applies here

The narrowest-reader rule checks whether every reader of a conversation may reach the governor. It does not look at what a reply contains.

The governor's replies are built from reads scoped to the requester (point 5). A clarifying question can name agents or recipes the requester may see. Shown in a group, it is shown to everyone there.

Today that adds nothing. The only group member policy is operators (ADR-0028 point 2), and every operator decides as the one operator subject, so every reader sees exactly what the requester sees.

A later member policy that admits subjects with narrower access than the requester changes that. A reader could then be shown names they could not list themselves. Such a policy must decide whether the governor's output may go to a group with those readers. Until one exists, no group can hold them.

## What this ADR deliberately does NOT decide

- Everything ADR-0032 left open stays open.
- Whether a reply that fails the reader rule in a group should fall back to the requester privately. ADR-0029 point 6 holds it back and never redirects it. A governor exception would be a change to that point, and is not made here.
- How the governor's output is handled in a group whose member policy admits subjects with narrower access than the requester. That is for the ADR that introduces such a policy.

## Consequences

- A request made in a group gets its proposal in the group, where the people who saw the request can see what it became. Any operator there may press it, as themselves.
- The governor's terminal pane and its tool calls still reach only the requester.
- One operator's request now reaches the other members of the group it was sent in, as the request itself already did. It reaches no one outside that conversation.
- ADR-0032 is superseded in full. Its points 1 to 3 and 5 to 8, its stderr rule and its deferred list carry over unchanged. Only point 4's rule for the governor's output is changed.
