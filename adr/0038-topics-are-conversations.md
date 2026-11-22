# ADR 0038: A group's topics are conversations of that group

**Status:** Proposed

Supersedes ADR-0023 point 9 (a topic-enabled group's topics map to channels), and only that point. Decides two items on ADR-0028's deferred list, which ADR-0029 keeps: topics, and a group's own default agent. Builds on ADR-0029 (output routing) and the group points of ADR-0028 it keeps. Leaves every other point of ADR-0023 and ADR-0029 unchanged.

## Context

Some platforms let a group be split into topics. Each topic is its own thread of messages inside the group, with its own place in the platform's interface. A topic is not a group of its own: on the platform the bot runs on today, a topic's messages carry the group's id and, beside it, the topic's id. A message sent with the group's id and no topic id lands in the group's general topic.

pao reads neither part of that today. It takes a group's conversation to be the group's id alone. So a message written in a topic is accepted as a message from its group, and the reply to it goes to the group's general topic, not the topic it was asked in. An agent cannot be bound to one topic, and an operator who sets an agent to answer in a topic sees its answers land somewhere else.

ADR-0023 point 9 decided topics another way: each topic would be a channel. It also said what that costs. A channel is one adapter with its own receive loop, and a platform whose credential allows a single polling consumer cannot run one loop per topic. So N topic channels on one credential need one receiver dispatching to N channels, a change to the one-adapter-per-channel shape. ADR-0028 deferred topics for that reason.

ADR-0029 has since made the conversation, not the channel, the thing output is routed to (point 1), and the thing a reply goes back to (point 2). A topic fits there with no change to the shape of channels: it is one more conversation of a channel, inside a group that is already one. That is what this ADR decides.

## Decision

1. **A topic is a conversation of its group.** A message written in a topic belongs to that topic's conversation, named by the group's id and the topic's id together. A message in a group without topics, or in a topic-enabled group's general topic, belongs to the group's own conversation, exactly as today. The adapter treats a message as a topic's only where the platform marks it as written in a topic. A platform may attach a thread id to other messages, such as a reply inside a group without topics. That id does not make a topic, and such a message is the group's as before. A private conversation has no topics and is unchanged.

2. **Every topic of an admitted group is served, and a topic the operator names is a destination.** Admitting a group (ADR-0028 point 1) serves every topic in it, as it does today. A topic has no member policy of its own: its readers are its group's, so the narrowest-reader rule (ADR-0028 point 5) is the same for a topic as for its group. A topic whose group is not admitted is refused, as its group is. A group's admission may also name some of its topics, each by the platform's topic id and a name of its own, unique among the node's groups and topics. A named topic is a destination, `group:<name>` as ADR-0029 point 1 names a group, so a standing binding or a default (point 5) can point at it. A topic that isn't named is still its own conversation for replies (point 3) and the last address (point 4); it just can't be set as a destination. Naming topics is administration at the operator surface, like admitting the group. When the platform moves a group to a new id (ADR-0028 point 1), its named topics are unlisted with it, and are named again when the operator lists the new id.

3. **A reply goes back to the topic it was asked in.** ADR-0029 point 2 applies to a topic as to any conversation: a message from a topic notes that topic as the origin, and the agent's output in that turn is sent into the topic. A standing binding to a topic (ADR-0029 point 3) sends into that topic. A command's answer and a button press (ADR-0026, ADR-0027) are a topic's when they come from it. A group's own conversation is sent to as today, with no topic named, so it lands in the general topic.

4. **A sender's last address is kept per conversation, and so per topic.** ADR-0028 point 4 keeps it per sender and per conversation. A topic is a conversation, so an address made in one topic does not steer what the same person types in another topic, in the group's own conversation, or privately.

5. **A conversation may have its own default agent, which comes before the channel's.** An admitted group or a named topic may have a default agent. A message there, or in an unnamed topic of a group that has one, that names no agent, answers no message and has no last address goes to that conversation's default, and only where it has none to the channel's default (ADR-0023 point 4, last in the precedence). It is set and cleared like a channel's default, at the operator surface and by a steering command from chat (ADR-0026). An agent may be the default of at most one conversation or channel, which keeps ADR-0023 point 3's single input: a conversation's default is the agent bound to it as input.

## What this ADR deliberately does NOT decide

- **Topics as channels.** ADR-0023 point 9's design, one channel per topic over one receiver, is withdrawn, not deferred. A topic is a conversation.
- **A member policy per topic.** A topic is read by its group's readers. A topic admitted to fewer or other people than its group would need the platform to keep them out, and pao cannot see who the platform shows a topic to.
- **Creating, renaming or closing topics.** That is the platform's, and the operator's in the platform. pao reads the topic id it is given and does not follow a topic's name.
- **Topics on other platforms.** This ADR is written against the shape described in Context: a topic id beside the group id on each message, and a send that names the topic. A platform that models threads differently gets its own decision when its adapter is built.

## Consequences

An operator can give each topic of one group its own agent: output, replies and plain text, without a second bot or a second group. A group without topics, and every private conversation, behave as before; the build's tests hold both as invariants. The adapter's id for a conversation grows a second part, so each place that reads it (sending, replies, button presses, the last address and the record of a moved group) changes with it. Point 5 adds a second place a default can live. The precedence stays one ordered list, and the channel's default remains the last step, so a node that sets no conversation default routes as today.
