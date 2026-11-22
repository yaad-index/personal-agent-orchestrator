# ADR 0023: Agent I/O — channel bindings and the default agent

**Status:** Accepted (2026-11-17)

**Point 9 superseded-by:** ADR-0038 (a group's topics are conversations of that group)

Extends ADR-0003 (routing), ADR-0008 (verbosity), ADR-0009 (channels), ADR-0012 (access), ADR-0013 (cross-node output), ADR-0019 (adapters + identity).

## Context

pao already has channels (ADR-0009), a transcript mirror with a per-kind verbosity level map in the agent profile (ADR-0008), routing by agent name and tag (ADR-0003), and human subjects scoped by an access matrix (ADR-0012). What it does not yet fix is how an agent is *bound* to channels. Today the human-in-the-loop line is one unified conduit (ADR-0009 point 5); which channel a given agent's output goes to, and which channel it takes input from, is left to the orchestrator's judgment.

A common deployment has a single agent serving several channels, with the agent itself choosing per message which channel to use. This ADR makes that binding explicit configuration and lets more than one agent participate, so such a deployment keeps all its channels while the per-channel choice of which agent answers, where output goes, and at what verbosity moves from an agent's discretion into config, and the single-agent experience is a subset of what is configurable.

## Decision

1. **An agent declares three I/O streams, by loose analogy to stdin / stdout / stderr.** The set is deliberately small and open to future extension.
   - **stdout** — output and its verbosity.
   - **stdin** — input.
   - **stderr** — the agent's own system/error line (config and permission-check notices).

2. **stdout is multiplexed to channels, each at its own verbosity level.** One agent's output may fan to several channels. Each binding carries a single level N, read against the profile's per-kind level map (ADR-0008 point 2): the channel shows every kind the profile places at or below N, so one channel sits at a basic level and another at full detail. The binding carries the level N, not a map of its own. A viewer's interactive backtrack (ADR-0008 point 3) still applies on top. Fanning a binding to a channel on another node is the cross-node output of ADR-0013 point 3.

3. **stdin is at most one channel per agent, and the default-agent role (point 5) IS that binding.** An agent has at most one channel bound as its input. A channel's default agent (point 5) is exactly the agent bound to that channel as stdin, so an agent is the default of at most one channel; the two are one binding seen from each end, not two facts that can disagree. In addition, any chat may address the agent explicitly with a pao command (point 6). A channel bound as stdin may also be one of the agent's stdout channels — input and output on the same channel or topic.

4. **An agent is routed to by a precedence of signals, default last.** An inbound in a channel may carry several routing signals; they resolve in order: an **explicit** target (point 6) first; then a **reply** to an agent's message, routed to that message's author; then the sender's **sticky** last explicit target, which the adapter keeps; and only when none of those is present, the channel's **default agent**. This is the destination-selection for input that names no target, which ADR-0003 deferred.

5. **A channel has a default agent, which answers the no-target case unprefixed.** When the precedence of point 4 falls through to the default, the message routes to the channel's default agent. The default agent's output carries no `[name]:` prefix — it reads as the channel's natural voice; an explicitly addressed, non-default agent's output is prefixed `[name]:`. The default is a single agent **by name, not a tag**, because a tag names a group. The operator switches a channel's default with a pao command.

6. **Addressing an agent is a pao command, not a platform mention.** `/pao` is pao's command prefix; an agent is addressed as `/pao agent <name> <message>`. A platform's own mention syntax is not reused, to avoid clashing with it. **Tag addressing is retained**: addressing by tag (ADR-0003 point 2, ADR-0012 point 4, fan-out bounded by access) stays available from chat through `/pao`; only the *default-agent* role (point 5) excludes tags. The exact tag form and a shorthand for the whole command are wanted and deferred.

7. **stderr is per-subject; a denial notice and an approval question are split.** An agent's own config and permission-check **notices** (e.g. "you may not do that") go to the subject who caused them, on the channel that subject's causing inbound arrived on (ADR-0019 point 4 lets a subject have bindings on several channels). An **approval question** (ADR-0004) is not an stderr notice to that subject: it must reach whoever may answer it — the operator — never a non-operator subject. So a denial is addressed to the subject, an approval prompt to the operator.

8. **A channel is a resource in the access matrix; one channel, many subjects (ADR-0012).** A channel (stdin and/or stdout) is a resource row, and any subject with access may use it. pao attributes each inbound to its subject (ADR-0019) and gates per message, so isolation comes from the matrix, not from a separate bot or front per subject. This decides what a channel grant means for a human beyond a front they may use, which ADR-0012 left open: a channel is a resource any granted subject may use for stdin and/or stdout.

9. *(Superseded by ADR-0038: a topic is a conversation of its group, not a channel.)* **A platform whose direct chat is a single conversation maps a topic-enabled group's topics to channels.** Where a platform's direct chat cannot carry several channels, each topic of a topic-enabled group is a channel, giving that platform the same channel model as one with native channels. **This is not free of adapter work.** A channel spec today builds one adapter with its own receive loop (ADR-0020 point 6); a platform whose credential allows only one polling consumer cannot run one loop per topic channel, so N topic channels on one credential need a single receiver dispatching to N channels — an adapter that serves several channels, which is a change to the one-adapter-per-channel shape, or a stated deferral. Topic traffic is also group traffic, so it depends on ADR-0019 point 6's group configuration (which subjects a group admits), deny-by-default until configured.

## What this ADR deliberately does NOT decide

The config schema for the bindings; the verbosity level numbers (ADR-0008 left them open); the exact tag-addressing form and the shorthand for `/pao agent <name>`; whether a channel's default agent is set at channel creation or only later; the single-receiver-serving-many-channels adapter shape (point 9), left to the build or a later ADR; the migration path from a single-agent deployment; and whether stderr's system channel is a distinct channel kind or a reserved per-subject binding. Each is deferred.

## Consequences

A single-agent deployment keeps every channel and gains explicit, per-channel control over which agent answers, where its output goes, and at what verbosity, with more agents addable without losing the single-agent experience — so the configurable model is a superset of it rather than a replacement that drops capability. A channel's default agent makes the common case (talk to the one agent that owns this channel) need no prefix or command, while naming an agent or a tag stays available everywhere. Making channels resources in the access matrix keeps multi-human scoping (ADR-0012) coherent with the I/O model; routing a denial to the subject but an approval question to the operator keeps the gate answerable while keeping one subject's notices off another's view. A platform whose direct chat is a single conversation joins through group topics, at the cost of a receiver that serves several channels and the group-admission configuration that topic traffic requires.
