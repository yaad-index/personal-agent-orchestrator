# ADR 0021: The composite message id

**Status:** Proposed (2026-11-16)

Extends ADR-0019 and ADR-0010.

## Context

ADR-0010 fixed that a message is addressed within pao by its id and that a reference from the human arrives id-first from its platform. ADR-0019 fixed that an inbound message carries the channel and the authenticated per-sender id. Neither says how a message that lives on a platform is named so that pao can point back at it: to reply to it, react to it, or edit it, the owning adapter needs the platform's own id for that message, and pao needs one handle for it that cannot collide with another channel's. This ADR fixes that handle.

## Decision

1. **Every channel has a numeric channel id.** It is a positive integer written in the channel's configuration, separate from the channel's name. The name stays the namespace of the bindings of ADR-0019 point 3; the id is only for addressing messages. Ids are unique within a node.

2. **A channel id is assigned once and never reused.** A channel's id does not change while the channel exists, and when a channel is removed its id is retired for good: the node remembers every id it has served and refuses a configuration that gives a retired id to a different channel. Reuse would silently point every old composite id at a different channel.

3. **A message on a channel is named by a composite id, `<channel-id>-<native-id>`.** The native id is the platform's own id for the message, kept exactly as the adapter gave it and treated as opaque: it may contain `-` or `:` or anything else the platform uses. The composite splits on its first `-` only, and the channel id before it is all digits, so the split is never ambiguous however the native id looks.

4. **The composite is globally unique within pao and reversible.** Unique, because channel ids are unique within a node and in a mesh the node qualifier of ADR-0017 point 10 goes in front, `<node>:<channel-id>-<native-id>`, read left to right with each delimiter taken at its first occurrence, which the node name and the channel id cannot contain. Reversible, because the owning adapter strips the prefix to recover the native id and acts on the original message on its platform, to reply, react or edit. An adapter acts only on composites that carry its own channel id; one that carries another's is refused.

5. **The communication layer forms the composite; the adapter supplies only the native id.** An inbound message reaches the layer with the channel and the native id of the message, and an adapter that sends a message reports the native id the platform gave it. The layer holds the channel id and builds the composite in this one place, so an adapter cannot name a message into another channel's space.

6. **The composite sits beside the pao message id; it does not replace it.** The pao id of ADR-0010 point 2 stays the canonical handle inside pao. The layer keeps a two-way map between composite and pao id over the same rolling window as the id map. A reference from the human by composite resolves to the pao id through it, and a reply or reaction pao makes on a platform resolves to the composite through it. A composite outside the window, or one naming a channel that is not configured, is unresolved and not guessed, with one answer for both so the difference is not observable.

7. **The composite is orchestrator-side.** Like the source kind (ADR-0001), it never reaches an agent in the message line. It reaches an agent only where the pao id does under ADR-0010 point 3, through a fetch.

## What this ADR deliberately does NOT decide

The form of any platform's native id. The size of the window, which stays ADR-0010's. Which platforms support reply, reaction or edit, and what an adapter does where one does not. The storage of the map. The channel configuration schema beyond the id (ADR-0020 point 6).

## Consequences

One handle names a message on any channel and cannot collide across channels or nodes, and the platform's own id is always recoverable from it, so pao can act back on a message where it lives. Because the channel id is separate from the name, bindings are undisturbed, and because ids are never reused, an old composite can fall out of the window but never point at the wrong channel. Keeping the pao id canonical leaves ADR-0010's quoting, fetch and reaction paths and every route already built on them as they are.
