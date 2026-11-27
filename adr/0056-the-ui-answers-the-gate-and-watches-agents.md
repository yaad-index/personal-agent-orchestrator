# ADR 0056: The UI answers the gate's questions and watches an agent's output

**Status:** Proposed

Extends ADR-0054 point 5 (driven from chat; set up, launched and bound from the UI), and settles the item it left open, answering approval questions and watching an agent's output from the UI. Builds on ADR-0047 points 4 and 5 (the UI is a front, not a new authority, and stays private), ADR-0004 (the gate), ADR-0027 point 6 (a settled question is closed everywhere it was shown), ADR-0046 point 4 (answering is the operator's) and ADR-0018 (the watch operation).

## Context

ADR-0054 made the UI the surface to set pao up, launch agents and bind them, and kept chat as how pao is driven day to day. It named answering approval questions and watching an agent's output from the UI as view and driving concerns beyond setup, and left them open.

Both already exist outside chat. The operator answers a question at the node's command line (`pao chat`) as well as in chat, and follows an agent's output with `pao watch`. Both are operations of the node's API that the UI could call. What is open is not whether the operator may do them, but whether the UI becomes a place where pao is driven and watched, not only set up.

An operator setting pao up meets both at once: a newly launched agent's first action that the gate holds waits for an answer, and the agent's output is how the operator sees that it works.

## Decision

1. **The UI answers the gate's questions.** It lists the questions waiting for an answer, with the agent, the action and when it was asked, and answers them: allow, always, or deny with a reason. An answer is the same operation chat and the command line run, under the same checks (ADR-0047 point 4). The first answer from any surface settles the question; a later one is refused as for a question no longer waiting, and the question is closed on every surface that showed it (ADR-0027 point 6).

2. **The UI watches an agent's output.** It shows an agent's rendered output at a level the viewer picks, the recent past first and then as it arrives, through the same watch operation as `pao watch` (ADR-0018), under the same access bound. A watch is a view: opening or closing it changes nothing for the agent or for anyone else's view.

3. **Both are the operator's, from the UI as everywhere.** The UI's actor is the operator (ADR-0047 point 4). Answering stays the operator's alone (ADR-0046 point 4). A UI actor other than the operator answering a question, or seeing an agent's output, is a decision of its own, taken when the UI authenticates other subjects.

4. **Chat stays the surface pao is driven from day to day.** The UI adds a front for these two and no authority, as for the rest of the UI (ADR-0054 point 5). Nothing moves off chat: a question is still announced and answerable there, and a binding still sends an agent's output to its channel.

## What this ADR deliberately does NOT decide

- Talking to an agent from the UI: sending it a message or a reaction.
- How the UI tells the operator that a question is waiting when its page is not open (a browser notification, a sound).
- The UI's screens for these, and how often a list is refreshed.
- A watch of an agent on another node from the UI, beyond what the watch operation already offers the operator at the master.

## Consequences

An operator who sets pao up in the UI can carry a new agent through its first held action, and see what it does, without leaving the page. The UI becomes a place to drive and watch, alongside chat, not instead of it. Because both are existing operations under existing checks, and the first answer from anywhere wins, two surfaces showing one question cannot give it two answers. The cost is a second place where a question can be answered, which the operator has to keep in mind when an answer they meant to give in chat says the question is no longer waiting.
