# ADR 0058: A channel is one conversation of one owner, on a provider the configuration defines

**Status:** Accepted (2026-11-28)

Supersedes ADR-0019, ADR-0029, ADR-0038, ADR-0045 and ADR-0057. Each is kept in full except the points named here, which this ADR changes:
- ADR-0019 points 4 and 6;
- ADR-0029 points 1, 4 and 7;
- ADR-0038 points 8, 9 and 10;
- ADR-0045 point 2;
- ADR-0057 points 2 and 3, which keep their rules and move from the channel to the provider.

Builds on ADR-0046 (channel ownership and who answers the gate), ADR-0047 (the UI as the setup surface), ADR-0055 (the managed file), ADR-0037 (secrets) and ADR-0012 (default-deny, no existence leak).

## Context

A channel was one bot on one platform. It served the operator's private conversation with the bot, every group the operator admitted, and every topic of those groups. Inside an admitted group, it acted on any member the group's policy admitted. An agent bound to such a channel heard every one of those conversations, and its output fanned out to them. The record of rejected inbound stayed empty, because none of these paths rejected anything.

That is wider than anyone means when they bind an agent to "a chat". An agent should hear the one conversation it was given, from the one person it belongs to, and nothing else. The platform connection, the bot and its token, is a different thing from the conversation it reaches. One connection can reach many conversations, and so carry many channels.

ADR-0045 point 6 already says an agent has at most one input, a conversation, a group or a named topic. This ADR makes the channel that input, and makes the channel the thing pao acts on.

## Decision

1. **A provider is a platform connection, defined in configuration only.** A provider is a platform pao reaches, such as a chat platform's bot. It holds the platform's configuration and the reference to its credential.
   - ADR-0057 points 2 and 3 apply to the provider's credential as they did to a channel's: it is a reference, never a value, and the place it is read from is the provider's alone.
   - A provider is defined only in configuration, never from the UI.
   - Its implementation lives in one sealed package per platform: sending, receiving and formatting a message. Nothing of the platform leaks into the rest of pao.

2. **A channel is a provider, one target on it, one owner and a unique name.**
   - The target is one conversation, in the provider's own shape: a private conversation with the owner, a group, or a topic of a group, as the platform has them.
   - The owner is the subject the channel belongs to (ADR-0012).
   - The name is unique on the node. It is how the channel is addressed everywhere else: bindings, defaults and the UI. A destination is a channel by its name. This replaces ADR-0029 point 1's `group:` and `private:` destinations, which named conversations within a channel.
   - A channel is defined in the UI by picking a configured provider, choosing the owner and entering the target in that provider's shape. It may also be written in configuration.
   - This replaces ADR-0019 point 4's "one channel serves many subjects": a provider serves many channels, and a channel serves its owner.
   - It replaces ADR-0038 point 10's "every topic of an admitted group is served": a channel is one topic, or one group, and no other.
   - It changes ADR-0038 point 9's "a topic is a conversation of its group, not a channel": a topic may now be a channel's target. The platform's single polling consumer is the provider, which serves every channel on it, rather than one adapter serving one channel. How a message is placed in a topic's conversation, by the platform's own mark, is unchanged.

3. **A channel defined in the UI is registered by a verify round-trip, not a plain save.** The UI's **Identify** action has pao send a one-time code to the target through the provider. The operator enters the code back into the UI, and only then is the channel saved.
   - The code is single-use and expires.
   - It proves the target exists and that whoever registers the channel can read it.

4. **A channel binds one agent, and pao routes the reply.** For now, an agent binds exactly one interaction channel; more than one is deferred.
   - The agent treats its channel as an opaque terminal. It receives a message with its sender and the channel it came from, and replies normally.
   - pao routes the reply back to the channel the message arrived on. The agent never routes, and never knows the provider or the platform.
   - This replaces ADR-0029 point 7's "any conversation may still address it explicitly": only the agent's channel reaches it.

5. **Only the channel's owner, in the channel's conversation, is acted on. Everything else is rejected.** An inbound is acted on only when it comes from the channel's target and from a sender bound to the channel's owner (ADR-0019 point 3).
   - Everything else is **rejected**: a conversation that is not a registered channel, and a sender other than the owner inside one.
   - A rejected inbound is recorded with its sender's id, the sender's name and the whole message (ADR-0019 point 7). The record is the operator's alone; no agent can read it. The UI shows the most recent ones, and the rest stay in the record.
   - A rejected inbound is never preprocessed (ADR-0045 point 5), never routed and never answered.
   - Registration (ADR-0019 point 5) is pao's own, as before. It happens in a private conversation, is never routed, and is how an owner binds a sender id.
   - This replaces ADR-0045 point 2, under which a group's member policy decided whose inbound was acted on, and ADR-0019 point 6, under which a group could admit several subjects.
   - It also replaces ADR-0038 point 8's channel used by any subject with access: a channel is used by its owner.

6. **Each user has a permission channel.** It is set when the user is defined: any channel, or by default a private conversation with the user. An agent belongs to a user and inherits the user's permission channel.
   - Gate questions about that user's agents, and pao's notices about them, are delivered there. This replaces ADR-0029 point 4's default of each operator's private conversation.
   - Who may answer a gate question is unchanged: ADR-0046 point 4 keeps it the operator's. A question for a user who may not answer it still reaches the operator.
   - The permission channel is the one kind of channel several of a user's agents may share.

7. **An agent may have one log channel. It is optional and off by default.** It receives the agent's output unfiltered, including detail that does not go to the interaction channel, for debugging and watching the agent.
   - It is output only: an inbound there is never routed to the agent, and is rejected under point 5.
   - It can be paused by a command, to throttle it, without being stopped.

8. **An interaction channel and a log channel each belong to one agent.** Neither is ever shared across agents. The permission channel of point 6 is the only kind that is shared.

9. **A channel can be bound only to an agent of its own user.** An agent belongs to a user, and a channel carries its owner. A channel owned by one user cannot be bound to an agent owned by another.
   - The owner holds the channel's ownership under ADR-0046 point 3. Until that ADR's per-channel ownership for a non-operator exists, an owner whose inbound is acted on must be operator-flagged, as ADR-0046 leaves it.

10. **A message may carry its sender as a prefix, by node configuration, off by default.** The setting is node-wide, and an agent may override it. It has three values:
    - off, no prefix;
    - the sender's name before each message, as `[name]`;
    - the sender and the channel, as `name@channel`.

    It is for when agents talk to each other. With one owner per channel, the sender is otherwise never in doubt.

## What this ADR deliberately does NOT decide

- An agent bound to more than one interaction channel, and a channel with more than one user. Both are deferred, and one agent binds one channel for now.
- Users and owners who are not the operator acting through a channel. That waits on ADR-0046's per-channel ownership, unchanged here.
- Each provider's target shapes beyond those named, and the form the Identify code takes on each platform.
- How long the record of rejected inbound keeps entries, and how many the UI shows, beyond showing the most recent.
- The log channel's pause command's syntax, and the prefix setting's names.

## Consequences

An agent hears one conversation from one person, and everything else that reaches its provider is recorded and goes no further. Adding a bot to a group, or another person writing in the channel's own conversation, reaches no agent. Because the channel is the unit, the rejected-inbound record shows exactly what was turned away and from where.

The cost is configuration:
- a conversation is a channel of its own, so a group and a private chat are two channels;
- moving the credential to the provider changes where the credential is written;
- a configuration written for the earlier model, with no owner and target, no longer loads. It fails closed rather than serving every conversation it can see.

Registering a channel from the UI takes a round-trip through the platform, which a plain save would not, in exchange for proving the target before pao acts on it.
