# ADR 0048: Budgeting is pao-native, and the governor is removed

**Status:** Proposed (2026-11-26)

Supersedes ADR-0034 (the governor replies where it was asked), and with it retires the governor entirely: ADR-0034 is the live head of the chain ADR-0030, ADR-0031, ADR-0032, ADR-0034, each superseding the one before, so superseding it retires the whole governor. In place of the governor this ADR keeps nothing; it removes it. Builds on ADR-0047 (the UI is the primary surface), ADR-0026 and ADR-0046 (the `/pao` command syntax, directly callable), ADR-0014 and ADR-0035 (budget), ADR-0007 (the control plane and its shed ladder), and ADR-0015 (the account).

## Context

The governor (ADR-0030, carried through ADR-0031, ADR-0032 and ADR-0034) is a natural-language front-end for operator commands: an always-on cheap-model agent that translates a natural-language request into one exact ADR-0026 command and shows it back for the operator to commit. It was introduced because the strict `/pao` syntax was the only direct way to steer pao and a natural-language shortcut was wanted; ADR-0030 kept the strict syntax authoritative and directly callable beside it.

The redesign removes the governor: with the UI now the primary surface for setting up what agents runs on (ADR-0047) and the `/pao` commands directly callable from chat and the command line (ADR-0026, ADR-0046), the natural-language-to-command front-end has no role left, having become a workaround for the missing surface rather than a capability of its own. The redesign pairs this with a plain statement that budgeting is pao's own and was never the governor's, which this ADR records so the removal is not read as touching budget.

## Decision

1. **The governor is removed.** The natural-language front-end for operator commands (ADR-0030 through ADR-0034) is retired: there is no governor agent and no `/gpao` prefix. Superseding ADR-0034, the live head of the governor chain, retires the whole chain; this ADR keeps none of its decisions and replaces them with the governor's absence.

2. **What the governor did is served by surfaces that already exist.** Issuing a command without the governor is the UI (ADR-0047, the primary setup surface) and the `/pao` command syntax, which ADR-0026 and ADR-0046 make directly callable from chat and the command line and which ADR-0030 always kept authoritative beside the governor. Nothing an operator could do through the governor is lost: the governor only translated and committed nothing of its own (ADR-0030), so its removal takes away a convenience, not an authority.

3. **The governor's mentions in other ADRs become moot, and break nothing.** Where an accepted ADR names the governor to exclude it, ADR-0041 point 1 ("a governor translation (ADR-0034) is not a task") and ADR-0037 point 1's like exclusion, the carve-out simply has nothing to carve once the governor is gone; no decision in those ADRs depended on the governor existing, so they stand unchanged and are not superseded here. The `/gpao` prefix is freed, with no command bound to it.

4. **Budgeting is pao-native, and was never the governor's.** pao owns budget and spend directly. It reads each account's authoritative figure from the provider by the account's method (ADR-0035), never an estimate and with no fallback, applies a per-account budget and the shed ladder (ADR-0014, ADR-0007) against that live remaining headroom, and routes budget through no agent and no helper. ADR-0035's read-usage-from-the-subscription stays pao's. The governor's removal does not touch any of this; ADR-0014 and ADR-0035 stand unchanged, and this point records that budget is pao's own so the removal is not misread as removing it.

## What this ADR deliberately does NOT decide

- The budget model itself, which ADR-0014 and ADR-0035 own and this ADR does not reopen.
- Whether a future natural-language helper could return in a different form once the UI is in place; this ADR removes the governor as defined, and does not design a replacement.
- What, if anything, the freed `/gpao` prefix is later used for.

## Consequences

The governor is gone: one always-on cheap-model agent and its `/gpao` prefix are removed, and the surfaces that remain, the UI (ADR-0047) and the directly-callable `/pao` commands (ADR-0026, ADR-0046), carry what it was used for. Because the governor committed nothing of its own (ADR-0030), removing it takes away a convenience and no authority, and because no accepted decision depended on it, the ADRs that name it (ADR-0041 point 1, ADR-0037 point 1) keep their wording with an exclusion that now has nothing to exclude. Budget is unaffected and stated plainly as pao's own: pao reads the provider's figure (ADR-0035) and runs the shed ladder (ADR-0014, ADR-0007) itself, with nothing delegated. This **supersedes ADR-0034** (and retires the governor chain ADR-0030 to ADR-0034 of which it is the head), and **builds on ADR-0047, ADR-0026, ADR-0046, ADR-0014, ADR-0035, ADR-0007 and ADR-0015**; it supersedes nothing else, and ADR-0014 and ADR-0035 stand unchanged.
