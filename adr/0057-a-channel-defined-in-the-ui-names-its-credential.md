# ADR 0057: A channel defined in the UI names its credential; the UI never handles one

**Status:** Accepted

Settles what ADR-0055 left open, where a channel's credential comes from when the UI defines the channel. Builds on ADR-0055 (the managed file), ADR-0047 points 2, 4 and 5 (the UI defines a channel, is a front and not a new authority, and stays private), ADR-0020 point 6 (a channel is configured with a reference to its credential, never the credential), ADR-0037 (the node's secrets, held in the daemon's environment or a file) and ADR-0053 point 1 (a secret bound to one consumer is that consumer's alone).

## Context

ADR-0047 has the UI define a channel, and ADR-0055 gives pao a file to write it to. A channel needs a credential to reach its platform: a bot's token, for instance. Today a channel's entry in the configuration names the environment variable that holds it, and the value is never in a configuration file.

ADR-0055 left open whether the UI may take the credential itself, for pao to store, or only name where it is. Taking it would put a long-lived secret into a browser form, through the node's HTTP API, and into a file pao writes, making the UI a path secrets travel. Naming it keeps the secret where the operator provisions every other secret, outside pao's own writing, at the cost of a step the operator does by hand.

## Decision

1. **The UI never takes, shows, stores or sends a channel's credential.** No UI page has a field for a credential's value, and no API call the UI makes carries one, in either direction. pao writes no credential to the managed file or to any file of its own on the UI's behalf.

2. **A channel defined in the UI names where its credential is.** The UI writes one of two references into the channel's entry:
   - an environment variable of the daemon's, as a channel names it today (ADR-0020 point 6);
   - one of the node's secrets by name (ADR-0037), whose value is in the daemon's environment or a file.

   The operator provisions the value there, by hand, as for any other secret. The reference is a name, and the UI may set and change it like any other field of the channel.

3. **A secret a channel names is that channel's alone.** No task may name it and no other channel or server may present it, as ADR-0053 point 1 rules for a server's secret. Otherwise a task naming the same secret would bring the channel's credential into an agent's run. The rule is on where the value is, not only on its name. The place a channel's credential is read from, an environment variable or a file, whether the channel names it directly or through a secret, is read for nothing else: no other secret, channel or server is held there. A configuration that breaks this, by name or by place, does not load.

4. **The UI may say whether the reference resolves, never what it resolves to.** For a channel's credential, the UI shows only whether the named variable or secret has a value pao can read. It shows no part of the value, its length or a hash of it.

## What this ADR deliberately does NOT decide

- Whether the channel's environment variable reference stays, or every channel credential becomes a node secret.
- Rotating a channel's credential, beyond pao reading it when it next starts the channel.
- Checking that a credential works, for example by connecting to the platform, before the channel is saved.
- A credential for anything other than a channel defined in the UI.

## Consequences

A channel can be set up from the UI without its credential ever passing through a browser, the node's API or a file pao writes. The secret stays where the operator already keeps pao's other secrets. The cost is one step outside the UI: the operator puts the value in the daemon's environment or a secret file, and restarts pao when it is new to its environment. Point 4 shows that step as done or not done, so the setup flow does not fail silently on a missing credential.
