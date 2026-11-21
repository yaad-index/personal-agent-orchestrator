# ADR 0033: A provisioned working directory's settings are not loaded, and trust is answered

**Status:** Accepted (2026-11-21)

Supersedes ADR-0025 (an agent's context comes from its profile). Keeps its points 2 to 5 and its consequences unchanged. Changes point 1 for a temporary agent's working directory. Settles the item of its deferred list on a backend's first-run trust. Extends ADR-0006 (temporary agents).

## Context

ADR-0025 point 1 lets an agent load its profile's own working directory, "its instructions and settings there". For a configured agent, that directory is the operator's own.

For a temporary agent it need not be. ADR-0006 point 2 provisions the working directory from a source, for example a clone of a git repository a recipe names. What is in it is that repository's, not the operator's. A backend's settings can define hooks, which are commands the backend runs. So loading a provisioned directory's settings would run commands the repository's author chose.

Today a first-run question stands in the way. On a directory it has not been told to trust, Claude Code asks whether to trust it before it loads anything there, and an unattended start times out at it. That keeps a cloned repository's hooks from running. But it also means a temporary agent on Claude Code never becomes ready, and that is the case for agents started on demand, where no one is at the terminal.

ADR-0025 left the first-run trust question open. For a configured agent, pao now answers it, since the directory is the operator's own. A temporary agent needs its settings kept out first.

## Decision

1. **By default an agent's context comes only from its profile and from pao.**
   - A backend is started so that it loads the MCP configuration pao gives it, and what pao installs itself (the gate's hook).
   - **A configured agent** also loads its profile's own working directory: its instructions and settings there.
   - **A temporary agent** also loads its working directory's instructions, but **none of the settings there**, from any of the backend's setting sources that live in the directory. What a provisioned directory holds comes from its source, not from the operator, so nothing in it may make the backend run a command. The adapter shuts those settings out (ADR-0025 point 2); a profile cannot open them.
   - It loads nothing from the node user's own environment:
     - no user-level or ancestor instruction files;
     - no user-scope MCP servers or account connectors;
     - no user skills, commands or plugins;
     - no user settings;
     - no memory the backend keeps across sessions.

6. **pao answers a backend's first-run trust question.** A backend that asks whether to trust a working directory, before it is ready, is answered yes. That holds for:
   - a configured agent, whose directory is the operator's own;
   - a temporary agent, whose directory's settings are shut out by point 1.

   The answer is the backend's own, given in its terminal, so the backend records the trust for that directory alone, never a parent. pao does not edit the backend's own record of trusted directories. An adapter whose backend cannot keep a directory's settings out does not have its trust question answered for a temporary agent, and that agent waits for a person.

## What this ADR deliberately does NOT decide

- The options each adapter uses to shut a directory's settings out.
- Isolation from the operating system, which stays the same-user limit `SECURITY.md` states.
- Whether a temporary agent's instructions, the text in its directory, need limits of their own. They make the backend do nothing by themselves, but they are text from the source that the model reads.

## Consequences

- A temporary agent on Claude Code becomes ready unattended, without running anything its source's author put in its settings.
- A recipe that relied on its repository's settings, such as a hook it ships, loses them. The recipe's own profile is where an agent's behaviour is set.
- ADR-0025 is superseded in full. Its points 2 to 5 and its consequences carry over unchanged. Point 1 is replaced as above, and point 6 is new.
