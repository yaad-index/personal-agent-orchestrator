# ADR 0028: Group conversations, operator-only first

**Status:** Proposed (2026-11-20)

Extends ADR-0012 (multiple humans and tag-scoped access), ADR-0019 (channel adapters and sender identity), ADR-0022 (operator subjects), ADR-0023 (agent I/O and channel bindings), ADR-0026 (operator commands from chat), ADR-0027 (action buttons).

## Context

A chat channel today serves one-to-one conversations only. Every message from a group conversation is recorded and refused, whoever sent it, and nothing is routed or answered. That is ADR-0019 point 6: a group is "the same shape with more configuration", and until the configuration exists it admits no one. The configuration was deferred. Output goes the same way: a channel sends an agent's output to the private conversation of each operator bound on it, and has no group to send to.

The operator wants one group conversation with pao's bot in it, and to run several agents from there, as a team runs its agents from a shared room. Addressing several agents from one channel already works: `/pao <name>`, `/pao agent <name>` and `/pao tag <tag>` (ADR-0026 point 9), a reply to an agent's message, and the sender's last address (ADR-0023 point 4). An agent that is not the channel's default speaks with its name in front of its output (ADR-0023 point 5). What is missing is the group itself.

The first group has one human in it, the operator. A group shared by several humans is the next step, and this ADR must not stand in its way. Admitting other people, bounding what a message shows by everyone who reads it, and giving people who are not the operator a place in the group must come later as a change of policy, not a redesign.

Some platforms limit what a bot receives in a group. By default such a bot sees only commands addressed to bots, replies to its own messages and mentions of it. Plain text is never delivered to it.

## Decision

1. **A group conversation is served only when the operator admits it, by the platform's id for the conversation.** A channel's configuration lists the group conversations it serves. Each entry names the conversation by the id the platform gives it, the same id a reply to it goes to. A group that is not listed is refused as today: its messages are recorded and routed nowhere, and nothing is said in it. Admitting a group is administration. It is done at the operator surface, like the channels themselves (ADR-0026 point 4), and never from chat. If the platform changes a conversation's id, the group is unlisted from then on and is refused until the operator lists the new id. It fails closed.

2. **An admitted group has a member policy. The first one admits only operators.** Each admitted group carries a policy that says which subjects may act in it. Its only value in this ADR is "operators": a message acts only if its sender resolves to a subject routed as the operator (ADR-0022). A message from anyone else in an admitted group is recorded, as an unadmitted sender's is today, and nothing is answered in the group. A refusal said in the group would be read by everyone in it. The policy is a field of the admission from the start, so admitting more people later is a new value of that field, not a new structure.

3. **Identity in a group is identity in a private conversation.** A sender is attributed by the platform's authenticated sender id, never the conversation's id (ADR-0019 points 2 and 3). So the operator writing in the group is the same subject as the operator writing to the bot privately, with the same access, and is checked the same way on every message. A group adds no identity of its own and no binding of its own. Registration is never accepted from a group: a code typed there is read by everyone in it, so the message is refused and not kept.

4. **A message in an admitted group is routed as it would be privately.** The routing precedence of ADR-0023 point 4 applies unchanged: an explicit `/pao` address, then a reply to an agent's message, then the sender's last address, then the channel's default agent. The last address is kept per sender and per conversation, so an address made in the group does not steer what the same person types privately. A `/pao` command is pao's in a group too (ADR-0026 point 1). A button press in an admitted group is a press as ADR-0027 defines it: the presser's own command, checked again when it arrives.

5. **Whether something may be shown in a conversation is decided by everyone who reads it.** A conversation's readers are its members under its policy: one subject for a private conversation, the admitted members for a group. pao sends a message into a conversation only if every one of its readers may see it. That is the narrowest-reader rule of ADR-0019 point 6, and it applies in three places:
   - **An agent's output** is shown only if every reader may reach that agent (ADR-0012).
   - **A gate question** is shown only if every reader may answer it. ADR-0023 point 7 sends a question only to the operator, never to anyone else. A group whose readers are all operators meets it. A group with any other reader does not, and the question goes to the operator's private conversation instead.
   - **A command's answer** goes to its sender on the channel it came from (ADR-0026 point 5). In a group it is shown there only if every reader may see it. Otherwise it goes to the sender privately where the platform allows, and is dropped where it does not.

   In a group whose policy is "operators" the rule always passes. It is defined over the reader set now, so admitting more readers later changes who passes and nothing else.

6. **Where an agent's output and a gate question go, once a group is admitted: for the operator to decide.** The channel's output can go to the admitted group, to each operator's private conversation, or to both. Lean: output goes to the group only, and a gate question goes to both.
   - Output in one place is the point of the group. Two copies of everything would bury it.
   - A question is the one message that cannot wait, and the operator may have the group muted. Showing it in both places makes it more likely to be seen within the ask timeout. ADR-0027 point 6 already closes the buttons on every copy once it is settled, so the second copy costs nothing after the answer.

   The choice is per channel, and a channel with no admitted group keeps sending to the operators' private conversations as today.

7. **Turning off the platform's group limit is a prerequisite that the operator sets up, not pao code.** Where a platform limits a bot in a group to commands and mentions, plain text in the group reaches pao only after the operator lifts that limit in the platform's own settings for the bot, or makes the bot an administrator of the group. pao does not change it and does not depend on it. Without it, `/pao` commands and replies to pao's messages still reach the bot, so the group works for commands and loses only plain text to the default or last-addressed agent.

## What this ADR deliberately does NOT decide

These are left for the multi-human ADR. Each is a new policy value or a new reader set under points 2 and 5, not a change to how a group is admitted, attributed or delivered to:

- **Admitting subjects who are not the operator** to a group, and how their membership is set: a list of subjects, a tag, or everyone holding a channel grant.
- **What an agent's output may show to a mixed group:** how the narrowest-reader rule of point 5 is computed from each member's scopes, and whether a message no single reader set allows is held back or split.
- **Where a gate question goes in a mixed group.** Point 5 already sends it privately. A later ADR may refine that.
- **Notices to a member who is not the operator** (ADR-0023 point 7) and whether a refusal is said to that person privately.
- **A group's own default agent.** The first build shares the channel's default agent between the group and the private conversations. Topics as channels (ADR-0023 point 9) stay deferred.
- **The configuration schema** for admitted groups, their policy and the output choice of point 6.

## Consequences

The operator can run every agent the node reaches from one group conversation, with the same commands, buttons and access as in a private conversation. The group becomes the window on the fleet. Nothing new is trusted: the group grants no access, a message in it is attributed to its sender exactly as a private one is, and an unlisted group is refused as before.

The multi-human group is a policy step. The admission already carries a member policy, identity is already per sender, and every delivery is already checked against the conversation's readers. Admitting a second person changes the policy's value and so the reader set. The rules that use the reader set stay as they are.

A group that is admitted but where the platform's group limit is still on accepts commands and replies, and silently misses plain text. pao is never sent that text, so it records nothing and cannot warn. The operator notices only when an agent does not answer, and closes the gap on the platform side.
