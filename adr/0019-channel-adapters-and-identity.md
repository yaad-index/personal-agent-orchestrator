# ADR 0019: Channel adapters and sender identity

**Status:** Proposed (2026-11-14)

## Context

ADR-0009 fixed what a channel is (the human-facing subset of sources) and that every source is an adapter in one place, but left each adapter's implementation, and which platforms ship first, open. ADR-0012 made humans subjects in the access matrix but explicitly deferred how an inbound sender is resolved to a particular human subject. ADR-0011 fixed how a backend is made pluggable, a uniform contract with the profile declaring the specifics, without binding pao's core to any one backend. This ADR fills those two gaps for channels: how a channel adapter is added, and how an inbound sender on a channel becomes a known human subject.

## Decision

1. **A channel adapter is a compile-time plugin behind one uniform contract.** A channel adapter implements the channel-source contract of ADR-0009 (inbound normalized to a sender-tagged message, outbound through the one conduit) the way a backend meets its contract in ADR-0011. Adding a channel is writing an adapter that registers itself against that contract; it ships by rebuilding pao, not by loading code at runtime. Runtime configuration only selects and configures the adapters already compiled in. Telegram and Discord are the first two; the contract is platform-generic, so a third such as WhatsApp is a new adapter and not a change to pao's core.

2. **Every inbound message carries an external sender id, in every channel, including a one-to-one.** The adapter tags each message with the platform's own identifier for who sent it (for a chat platform, the per-conversation or per-account id it exposes). Carrying it unconditionally, rather than only when a conversation has more than one participant, is what lets one conversation shape cover both the one-to-one and the group case.

3. **Identity is a binding from (channel, external sender id) to one human subject, created once at registration.** The pair of a channel and an external sender id resolves to exactly one human subject of ADR-0012. The binding is established at registration, and registration is where the human is authenticated; after it, an inbound message is attributed to its subject by looking the pair up, with no per-message auth. This is the sender-to-subject resolution ADR-0012 left open.

4. **One channel serves many subjects, and one subject is reachable on many channels.** Because the binding keys on the external sender id and not on the channel alone, a single channel (one bot on one platform) carries several humans, each told apart by their own id and each its own subject. Symmetrically, one human may hold a binding on several channels, the same subject reached on two platforms, each binding independent. A channel is therefore not a human; it is a front on which bindings live.

5. **An unbound sender has no access.** An external sender id with no binding resolves to no subject, and under the default-deny of ADR-0005 and ADR-0012 reaches nothing; consistent with ADR-0012, this is not distinguishable from a sender that does not exist, so an unregistered sender cannot probe for what is present. Registration is the only way a sender gains a subject.

6. **A conversation shared by several humans is the same shape with more configuration.** A group conversation is one channel endpoint whose inbound messages carry different sender ids, each resolving to its own subject by point 3, with attribution per message by point 2. What it adds over a one-to-one is configuration, not a new mechanism: which subjects the conversation admits, and how a resource scope (the tags of ADR-0012) applies to a message whose readers are several subjects at once. That configuration is deferred here, and the multi-human conversation is deliberately the more-configured case.

## What this ADR deliberately does NOT decide

The access and grant semantics for a subject, including what a channel grant means (ADR-0005, ADR-0012). The message model and how a sender tag renders to the agent as `[sender]: text` (ADR-0010). The outbound policy on the conduit (the budget-and-outbound-notifications ADR). Routing of an inbound to an agent (ADR-0003). The exact adapter registration API, the per-channel configuration and credential schema, and the registration and authentication procedure itself. The group-conversation configuration (which subjects, and the scope semantics for a shared-reader message). Which node holds a channel's credentials (ADR-0013, ADR-0017).

## Consequences

A new channel is a self-registering adapter plus a rebuild, with pao's core staying platform-neutral, the same shape ADR-0011 gave backends. Keying identity on (channel, external sender id) rather than on the channel lets one bot serve several humans and one human span several platforms, which is the resolution ADR-0012 deferred, while the default-deny keeps an unbound sender both powerless and unable to probe. Carrying a sender id even in a one-to-one makes a group conversation an extension by configuration rather than a separate code path, so the one-to-one and the many-human case share one mechanism.
