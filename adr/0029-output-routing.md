# ADR 0029: Output routing — replies go where they were addressed, named destinations per stream

**Status:** Proposed (2026-11-20)

Supersedes ADR-0028 (group conversations). Keeps its points 1 to 5, its prerequisite and its deferred list unchanged, and changes point 6. Extends ADR-0008 (verbosity), ADR-0012 (access), ADR-0023 (agent I/O and channel bindings), ADR-0026 (operator commands from chat).

## Context

ADR-0023 gives an agent three streams. Its output (stdout) may fan to several channels, each at its own verbosity level. Its input (stdin) is at most one bound channel, plus any explicit address. Its notices and approval questions (stderr) go to the subject who caused them, or to the operator. When ADR-0023 was written, a channel was in practice one conversation: each operator's private chat with the bot.

ADR-0028 made a channel hold several conversations: each operator's private conversation and any admitted groups. Its point 6 chose a deliberately minimal default. Once a channel has an admitted group, all of an agent's output goes to the group, and a gate question goes to the group and to each operator privately. That default is now the problem. An operator who writes to an agent in a private conversation gets the answer in the group. The reply leaves the conversation it belongs to, and a private exchange is shown to everyone the group admits.

Two more needs are known.
- The operator wants to watch one agent in several places at once at different levels. For example, a group at the normal level and a second group at full detail, with the agent's errors and questions in the operator's private conversation.
- Some deployments want every message of a binding marked with a short label of the operator's choosing.

A group now has a name, given in its admission, which makes it addressable without its platform id.

The streams are the ones ADR-0023 names, and this ADR keeps its model:
- stdout fans out to many destinations, each at its own level.
- stderr and gate questions are routed separately.
- stdin is single. Keeping two inputs to one agent apart is the operator's job, not pao's.

## Decision

1. **A destination is a conversation, and it has a name that says its kind.** pao routes output to conversations, not to channels as a whole. A destination name carries its kind, so the two sources of names cannot collide:
   - An admitted group is `group:<name>`, by the name in its admission. Group names are already unique within a node.
   - An operator's private conversation is `private:<subject>`, by the subject it belongs to. Subject names are already unique within a node. Where the subject has bindings on several channels, the channel's name qualifies it: `private:<subject>@<channel>`.

   A group and a subject may share a bare name without ambiguity, because the kind is part of every destination name. No check across the two stores is needed. The operator uses these names wherever a destination is set or shown, and the platform's ids stay inside the adapter and the admission.

2. **A reply goes back to the conversation it was addressed from.** When pao delivers a message to an agent, it notes the conversation the message came from as the origin of the agent's turn. Output from the agent during that turn goes to the origin. A private message is answered privately and a group message in the group. This replaces ADR-0028 point 6 for agent output. It holds for a message addressed by name, by tag, by reply or by the default agent alike. A message from the node's local channel or the operator surface has the local channel as its origin.
   - A turn ends when the next message is delivered to the agent. Output after that belongs to the new origin.
   - pao does not try to tell apart two conversations writing to the same agent at once. That is the single-stdin rule (point 7).
   - Output that no delivered message started has no origin. Examples are an agent's own scheduled work, or output after a restart before any message arrives. It goes only to the agent's standing bindings (point 3).

3. **On top of the reply, configuration decides who else follows an agent: standing bindings, each a destination at its own level.** Routing follows the configuration, not where the operator happens to write. A standing binding subscribes a named destination to an agent's stream at a verbosity level, read against the profile's level map as ADR-0023 point 2 sets it. An agent may have several bindings, one, or none, in which case only the origin hears it. Bindings apply to all its output, with or without an origin.
   - Verbosity is a scale. Its highest level, log, mirrors the agent's whole stream, including the messages delivered to it, each shown with its sender, and not only what the agent says. A lower level shows less of the agent's output and none of its input.
   - The origin gets the reply at its own level: the level of a standing binding to the same destination if there is one, otherwise the channel's level.
   - A destination that is both the origin and a standing binding gets one copy, not two.
   - Standing bindings refine ADR-0023 point 2's channel binding to the conversation. Binding a channel as a whole means every conversation on it that may show the output (point 6).

4. **stderr and gate questions are routed separately from stdout.** An agent's stderr has its own destinations, set like stdout's. A gate question goes to the agent's stderr destinations. By default those are the private conversation of each operator bound on a channel, as before any group was admitted. A question still reaches only those who may answer it (ADR-0023 point 7, ADR-0028 point 5). A notice that is not a question goes to the subject who caused it, on the conversation the causing message came from, as ADR-0023 point 7 already decides. A question shown in several places is closed on every copy once it is settled (ADR-0027 point 6).

5. **A binding may carry a prefix the operator chooses.** A standing binding, and the origin reply of the agent it belongs to, may carry a prefix of at most 10 characters. It is put before every message the binding sends. Where it is set, it replaces the `[name]:` rule of ADR-0023 point 5 for that binding. Where it is not set, that rule applies unchanged: the channel's default agent is unprefixed and every other agent is named.

6. **Every destination is checked against everyone who reads it.** The narrowest-reader rule of ADR-0028 point 5 applies to each destination: the origin, every standing binding, every stderr destination. A conversation is sent a message only if every one of its readers may see it. For an agent's output, that means every reader may reach the agent (ADR-0012).
   - A reply whose origin fails the rule is not sent there. It is held back and recorded, so the operator can see it. It is never redirected somewhere else.
   - A standing binding that fails the rule sends nothing to that destination. This applies to mirrored input at the log level too: a message delivered to an agent is shown in a destination only if every reader there may see it. For example, a private message mirrored to a group passes only when everyone the group admits may read it.
   - Routing chooses where a message may go. The reader rule decides whether it goes.

7. **stdin stays single, and keeping inputs apart is the operator's job.** An agent has at most one bound input channel, and any conversation may still address it explicitly (ADR-0023 points 3 and 6). When two conversations write to the same agent, its turns follow the order the messages arrive in, and each turn's output goes to its own origin. pao does not merge, queue by conversation, or tell apart inputs that arrive while a turn is running. Keeping those unambiguous is the operator's job.

8. **Bindings are set by name, from the operator surface or from chat.** A standing binding names an agent, a stream (stdout or stderr), a destination by name, a level and an optional prefix. Setting one from chat is a steering command under ADR-0026: within the operator's access, answered to the sender alone. Listing an agent's bindings shows destinations by name.

## What this ADR deliberately does NOT decide

- **The configuration schema and the command syntax** for bindings, destination names and prefixes, and how a binding is stored across restarts.
- **The level numbers,** which ADR-0008 left open.
- **How a remote agent's origin travels.** An agent on another node keeps its own origin across the mesh. The wire for it belongs to the mesh ADRs.
- **Turns that interleave.** pao routes by the most recently delivered message and does not track an agent's internal work beyond that. A backend that reports turn boundaries itself may sharpen this later.
- **A translating agent** that turns the operator's plain words into pao commands. It is adjacent to this ADR and would take its own ADR. It would use the destination names decided here.
- **Everything ADR-0028 deferred** for groups with several humans. Its list is kept.

## Consequences

An answer stays in the conversation it was asked in. A private question is answered privately even on a channel with an admitted group, and a group question in the group. The operator can watch an agent in as many places as are useful, each at its own level, and keep its questions and errors in one private place. Destinations are named, so the operator never handles a platform id after admission.

Every destination passes the reader rule, so routing more widely never shows a message to someone who may not see it. The worst a wrong binding does is send nothing, which the record shows.

One thing is assumed rather than tracked: that an agent's output during a turn belongs to the message that started it. Where an operator sends from two conversations at once, the second message starts a new turn. Output still in flight from the first may then go to the second conversation. pao does not prevent that; it leaves it to the operator, as point 7 says.

ADR-0028 is superseded in full, as the index requires, and its points 1 to 5, its prerequisite and its deferred list carry over unchanged. Only its point 6, the group default, is replaced.
