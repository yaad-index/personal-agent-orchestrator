# ADR 0048: Budgeting is pao-native, and the governor is removed

**Status:** Proposed (2026-11-26)

Supersedes ADR-0034 (the governor replies where it was asked), and with it retires the governor entirely: ADR-0034 is the live head of the chain ADR-0030, ADR-0031, ADR-0032, ADR-0034, each superseding the one before, so superseding it retires the whole governor. In place of the governor this ADR keeps nothing; it removes it. Builds on ADR-0047 (the UI is the primary surface), ADR-0046 (the `/pao` command syntax, directly callable), and ADR-0014 and ADR-0035 (budget).

## Context

The governor (ADR-0030, carried through ADR-0031, ADR-0032 and ADR-0034) is a natural-language front-end for operator commands: a cheap-model agent, started on demand (ADR-0031), that translates a natural-language request into one exact command and shows it back for the operator to commit. It was introduced because the strict `/pao` syntax was the only direct way to steer pao and a natural-language shortcut was wanted; ADR-0030 kept the strict syntax authoritative and directly callable beside it.

The redesign removes the governor: with the UI now the primary surface for setting up what agents run on (ADR-0047) and the `/pao` commands directly callable from chat and the command line (ADR-0046), the natural-language-to-command front-end has no role left, having become a workaround for the missing surface rather than a capability of its own. The redesign pairs this with a plain statement that budgeting is pao's own and was never the governor's, which this ADR records so the removal is not read as touching budget.

## Decision

1. **The governor is removed.** The natural-language front-end for operator commands (ADR-0030 through ADR-0034) is retired: there is no governor agent and no `/gpao` prefix. Superseding ADR-0034, the live head of the governor chain, retires the whole chain; this ADR keeps none of its decisions and replaces them with the governor's absence.

2. **What the governor did is served by surfaces that already exist.** Issuing a command without the governor is the UI (ADR-0047, the primary setup surface) and the `/pao` command syntax, which ADR-0046 makes directly callable from chat and the command line and which ADR-0030 always kept authoritative beside the governor. Nothing an operator could do through the governor is lost: the governor only translated and committed nothing of its own (ADR-0030), so its removal takes away a convenience, not an authority.

3. **The governor's mentions in other ADRs become moot, and break nothing.** ADR-0037 point 1 excludes a governor translation from being a task, and ADR-0041 point 1 leaves the governor outside its scope; once the governor is gone, neither has anything to exclude. No decision in those ADRs depended on the governor existing, so they stand unchanged and are not superseded here. The `/gpao` prefix is freed, with no command bound to it.

4. **Budgeting is pao-native, and the governor never touched it.** pao owns budget and spend as ADR-0014 and ADR-0035 already define, and those ADRs stand unchanged. The governor only translated commands and had no budget role, so removing it does not touch budget. This point records that only so the removal is not misread as removing budget, and it reopens nothing in ADR-0014 or ADR-0035.

## What this ADR deliberately does NOT decide

- The budget model itself, which ADR-0014 and ADR-0035 own and this ADR does not reopen.
- Whether a future natural-language helper could return in a different form once the UI is in place; this ADR removes the governor as defined, and does not design a replacement.
- What, if anything, the freed `/gpao` prefix is later used for.

## Consequences

The governor is gone: one cheap-model agent and its `/gpao` prefix are removed, and the surfaces that remain, the UI (ADR-0047) and the directly-callable `/pao` commands (ADR-0046), carry what it was used for. Because the governor committed nothing of its own (ADR-0030), removing it takes away a convenience and no authority, and because no accepted decision depended on it, the ADRs that name it (ADR-0037 point 1 and ADR-0041 point 1) keep their wording with an exclusion that now has nothing to exclude. Budget is unaffected and stays pao's own, exactly as ADR-0014 and ADR-0035 already define it, with nothing reopened here. This **supersedes ADR-0034** (and retires the governor chain ADR-0030 to ADR-0034 of which it is the head), and **builds on ADR-0047, ADR-0046, ADR-0014 and ADR-0035**; it supersedes nothing else, and ADR-0014 and ADR-0035 stand unchanged.
