# ADR 0022: Operator subjects

**Status:** Proposed (2026-11-16)

Extends ADR-0020.

## Context

ADR-0020 point 9 leaves a registered human's inbound to be attributed and routed under the access seam's current default, and ADR-0012 point 1 gives a human subject no access until it is granted. Until the access matrix exists the seam is operator-only: the operator may do anything and every other subject nothing. Read together, a human registered on a channel reaches nothing, including the human who runs pao, whose own chat id is a plain subject like any other. ADR-0020 does not decide the access and grant semantics for a subject, so how a human subject comes to act as the operator is open, and it is a grant semantic.

## Decision

1. **A subject may be flagged operator.** The flag marks a human subject as a person acting as the operator of ADR-0005, the one identity the operator-only seam allows to do anything.

2. **Only the operator surface sets or clears the flag.** It is set and cleared at the operator's local surface and nowhere else. A registration cannot carry it, an invitation cannot carry it, a channel cannot set it, and an agent cannot. A subject is created unflagged, and a human who registers is unflagged unless the operator has flagged that subject.

3. **An inbound from an operator-flagged subject is routed as the operator.** The sender resolves to a subject by ADR-0019 point 3, and if that subject is flagged the inbound is presented to the seam as the operator. It is attributed under its own subject name, the sender tag of ADR-0010, so several flagged humans stay distinct to the agents and in the record.

4. **Every other registered subject stays denied.** A subject that is not flagged is presented to the seam as itself, which the operator-only seam denies. Its inbound is not routed and is recorded as outside access (ADR-0019 point 7). This ADR grants nothing to such a subject; grants are the access-enforcement work (ADR-0005, ADR-0012).

5. **The flag is read at each inbound.** Whether a subject is flagged is looked up when a message arrives, not remembered from registration. Clearing the flag, revoking the binding, or removing the subject takes effect on the next message from that sender.

6. **The flag is an explicit grant, not a default.** It is consistent with ADR-0012 point 1: no subject has access by default, and the flag is one operator act on one named subject that widens nothing for any other. When the access matrix lands, whether the flag stays, becomes a grant, or is replaced is that work's to decide, and its ADR supersedes or narrows this one.

## What this ADR deliberately does NOT decide

The access and grant semantics for any subject beyond this flag (ADR-0005, ADR-0012). Which operations a channel exposes to a flagged subject beyond sending a message to an agent and answering a gate question it is shown. How the flag is shown or managed in the management UI (ADR-0005).

## Consequences

The human who runs pao can use a chat channel as themselves, and several such humans stay separate in attribution, without the access matrix being pulled into the chat build. Because only the local operator surface sets the flag, no message from a channel can raise a sender's standing, and an invitation can only bind a sender to a subject, never make the subject an operator. Every subject that is not flagged stays denied and visible in the rejected-inbound record, so nothing is silently dropped and nothing is granted by accident.
