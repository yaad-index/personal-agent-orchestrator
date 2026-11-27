# ADR 0052: Sending a file from an agent's working directory

**Status:** Accepted

Extends ADR-0026 (chat commands), ADR-0027 (action buttons) and ADR-0050 (agent Markdown rendered per channel). Builds on ADR-0010 (the message model and its attachments), ADR-0012 (access bounded by what a subject may reach), ADR-0019 (channel adapters) and ADR-0025 (what an agent's working directory is).

## Context

An agent that works on files refers to them in what it writes, often as a Markdown link to a path in its working directory, such as a note in a notes folder. ADR-0050 shows a link only when its address is absolute. A path in the working directory means nothing on a chat platform, so the reader sees the link's text and the file stays out of reach. The human then has to ask for the content, and the agent pastes it.

pao can't send a file to a channel today. A message carries attachments in the model of ADR-0010, but nothing hands an agent's file to a channel. Keeping a human's own copy of the working directory in step with the agent's is a different and larger thing, and it is not pao's to do.

## Decision

1. **`/pao file <agent> <path>` sends a file from the agent's working directory to whoever asked.** The file goes to the sender alone, on the channel the command came from, as a command's answer does (ADR-0026 point 5). It arrives as a file the platform lets the reader open or save.

2. **Anyone who may address the agent may ask for its files.** The command is open to every subject the access matrix lets address that agent (ADR-0012), not to the operator alone. This extends ADR-0026 point 3, which leaves a subject that isn't the operator only help and addressing: reading what an agent wrote is part of talking to it. An agent the sender may not address is answered exactly as an agent that does not exist (ADR-0012 point 4).

3. **Only a file inside the working directory can be sent.** The path is read relative to the agent's working directory and resolved through every symbolic link before it is checked. A path that resolves outside the working directory, names anything inside `.git` or `.claude`, or is not a regular file is refused, and so is one over the channel's size limit. The same refusal is given whether the file is missing or forbidden, so the command can't be used to probe the host.

4. **A link to a file in the working directory becomes a button for that command.** When an agent's Markdown links to a relative path that names a file in its working directory, the message carries an action (ADR-0027) labelled with the file's name, whose command is `/pao file <agent> <path>`. Pressing it is the typed command from the presser, checked as in points 2 and 3. The link's text stays in the message as ADR-0050 renders it. The layer, which knows the agent and its working directory, decides which links name such files. An adapter only shows the actions. A channel that shows no buttons still has the typed command.

5. **A channel that can't send files says so.** Sending a file is an adapter capability. On a channel without it, the command answers that this channel can't carry files, and no button is offered.

## What this ADR deliberately does NOT decide

- Keeping a copy of the working directory in step with the human's own. That is outside pao.
- Sending a directory, or several files as one archive.
- The size limit on each platform, beyond refusing what the channel cannot carry.
- An agent sending a file on its own initiative through its tool interface. This ADR covers only what a human asks for, typed or pressed.
- How many buttons a long message carries before the rest are left as typed commands.

## Consequences

A human can open the file an agent refers to with one press, without asking the agent to paste it. What the agent wrote stays where it is, and pao only reads the file at the moment it is asked for.

Points 2 and 3 widen what a subject that isn't the operator may do: whoever may address an agent may now read any regular file in that agent's working directory outside `.git` and `.claude`. An operator who gives a subject access to an agent should know that includes its files. Anything an agent must keep from the people who talk to it does not belong in its working directory.
