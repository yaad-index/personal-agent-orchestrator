# ADR 0045: Channel membership as a reader policy, and per-type inbound preprocessing

**Status:** Accepted (2026-11-25)

**Superseded-by:** ADR-0058 (a channel is one conversation of one owner; changes point 2)

## Context

The channel model is already rich: a channel is a communication endpoint with a per-source adapter (ADR-0009, ADR-0019); every inbound carries an authenticated per-sender id resolved to a subject (ADR-0019); a channel is a resource used for stdin and stdout (ADR-0038 point 8); a `/pao` command is pao's and never reaches an agent, with who may run one decided per command from the sender's subject (ADR-0026); and a group has a member policy whose only value today is "operators", a conversation's readers being its members under the narrowest-reader rule (ADR-0028 points 2 and 5, kept by ADR-0029).

This ADR fixes two things the redesign sharpens: who counts as a member of a channel, and how a non-text inbound reaches an agent that reads normalized messages. The separate question of who may **command** a channel (its ownership) is drawn in a following ADR, because changing who may steer touches ADR-0026 and the index requires that to be a full supersession, which is its own ADR's job.

## Decision

1. **Comms and access are separate.** A channel is a communication endpoint (ADR-0009, ADR-0019), and pao is multi-source. That a channel is a place messages flow is one thing; what a subject may do there is access, decided by the matrix (ADR-0005, ADR-0012), and the two are kept apart.

2. **Membership is a reader-and-participant policy.** Membership is a value of the group member policy of ADR-0028 point 2 (kept by ADR-0029), whose only value today is "operators": a channel's members are the subjects its policy admits, and they are its readers under ADR-0028 point 5's (kept by ADR-0029) narrowest-reader rule, which decides whose output and which questions may be shown there. pao cannot control who a platform shows a group to, so membership decides who counts as a reader and whose inbound is acted on, not platform visibility. Whether a member's own text reaches the agent is the stdin side of ADR-0038 point 8's channel use, which membership grants where the policy admits the member to act.

3. **Who may command a channel is a following ADR.** This ADR fixes membership, the grant to read and participate. The separate grant for who may issue the steering commands of ADR-0026 point 3, a channel's ownership, is drawn in a following ADR, which supersedes ADR-0026 to make that change (the index's all-or-nothing rule). Until then, commands stay exactly as ADR-0026 has them: steering and answering an approval question are the operator's (ADR-0026 point 3, ADR-0038 point 7, ADR-0004), and a non-operator keeps `/pao help` and addressing the agents it may reach.

4. **A command is handled by pao, not delivered to an agent; agent-addressed text is.** A `/pao` command is pao's to handle and is never passed to an agent (ADR-0026 point 1). Only text addressed to an agent, explicitly or by the routing precedence of ADR-0038 point 4, reaches it, as the normalized message of ADR-0010. This matters for point 5: what the preprocessor feeds an agent is agent-addressed content, never a command.

5. **An admitted non-text inbound is preprocessed by type before the agent.** Once a sender is attributed and admitted (ADR-0019 points 3 and 5, ADR-0028 point 2 kept by ADR-0029), a non-text inbound, a file, a voice clip, an image, is run through a per-type **preprocessor** pao applies first: a voice clip becomes its transcript text, an image a stored reference the agent can open, each type its own preprocessor. The agent receives the normalized result (ADR-0010) and never the raw upload in its message, and an admitted inbound of a type that has no preprocessor is refused. A **rejected** inbound, from an unbound or unadmitted sender, is recorded raw and bounded (ADR-0019 point 7) and is **never preprocessed**, so an outsider cannot make pao transcribe audio or store a file.

6. **An agent has at most one input.** An agent's input is at most one channel or conversation, a group or a named topic (ADR-0038 point 3), its home; this holds for the per-user orchestrator (ADR-0040) and any other agent alike. It names the input an agent reads and does not change ADR-0038's stdout fan-out or routing precedence.

The internals left open: the member-policy values beyond "operators" and how membership is granted, with the access matrix (ADR-0005, ADR-0012); the preprocessor set and how a type maps to one, where a preprocessor's output lives and under what access; and whether a preprocessor may be a driver of an ADR-0039 plane or stays pao-internal. Channel ownership, who may command, is the following ADR's subject and is not decided here.

## Consequences

Membership names who reads and participates in a channel, as a value of the member policy that already governs a group's readers, so who counts as a reader is a grant rather than left to whatever the platform happens to show, even though pao cannot change what the platform shows. Preprocessing an admitted inbound by type means an agent sees normalized text or a reference and never a raw upload, a new input type is a new preprocessor, an unknown type fails closed, and an outsider's content is recorded raw rather than processed. Holding the command/ownership change for its own ADR keeps this one from having to supersede ADR-0026. This **builds on ADR-0009 and ADR-0019** (the endpoint and sender identity), **ADR-0038** (the channel resource, the streams, the routing, and the operator-only gate answer), **ADR-0028 as kept by ADR-0029** (the member policy and narrowest-reader rule), **ADR-0026** (commands decided per subject, left unchanged here), **ADR-0010** (the normalized message), **ADR-0004** (the gate), and **ADR-0005 and ADR-0012** (the grants); it supersedes nothing.

The cost is a preprocessor per input type to build and keep, failing closed on an unknown type and never running on a rejected inbound; and that the channel picture is complete only once the following ownership ADR lands, since this one draws the read-and-participate half and leaves the command half to it.
