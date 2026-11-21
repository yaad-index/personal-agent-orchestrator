# Budget

How pao knows what is left on each account its agents draw on (ADR-0035).

## Configuring accounts

In the orchestrator config:

```yaml
accounts:
  - name: sub
    metered: true
    reader: claude-subscription
    credentials: /path/to/the/login/.credentials.json
  - name: local
    metered: false
budget:
  poll: 5m
  max_stale: 15m
```

- `metered` must be set on every account. `false` declares the account unmetered: its agents always have headroom, and it is never read.
- A metered account names its `reader` and the `credentials` file that reader reads. The `claude-subscription` reader reads the coding-agent harness's credentials file for the login. A relative path is resolved against the config file's directory.
- `budget.poll` is how often each metered account is read. The default is 5m.
- `budget.max_stale` is how old the last good figure may grow while reads fail for a reason that passes, before the account is at fault. The default is 15m, and it is never less than `budget.poll`.

A profile names the account its agent draws on, and how its headroom is judged:

```yaml
account: sub
budget:
  windows: [week, five_hour]
  floor: 10
```

- `windows` lists the windows weighed. Leaving it out weighs every window.
- `floor` is the share, in percent, each weighed window must keep above. The default is 0, so any share left is headroom.

## What is read

The `claude-subscription` reader reads the share used of the account's weekly and five-hour windows, as the account itself reports them. That figure covers the whole plan: every agent on the login counts toward it. pao shows and judges the share left, which is 100 minus the share used.

The reader reads the access token from the credentials file on every read and never writes the file. The harness refreshes its own token. pao never refreshes it, because a second refresher would rotate the refresh token out from under the harness and log the login out.

## Faults

A metered account whose figure cannot be read is at fault. That covers a refused token, a window missing from the answer, an answer that isn't the expected one, or no reader for it in this build. Work already running on it keeps running. The account has no headroom until it reads again. Each operator is told privately, never in a group, when an account enters a fault and when it leaves one.

A failure that passes is not a fault at first: a rate limit (HTTP 429), a server error (5xx), or no connection. The last good figure is kept, and headroom is judged on it. `pao budget` shows the failure and when the account is read again. Reads back off: each wait doubles from `budget.poll` up to 30 minutes, and is never shorter than a `Retry-After` the endpoint gives. No one is told while the kept figure is no older than `budget.max_stale`. The moment it is older, the account is at fault and grants no headroom, whether or not a read has been tried since, and the operator is told at the next poll; a `Retry-After` longer than that is still honoured before the next read. With no good figure at all, the account is at fault.

The same rule covers an agent whose profile names no account, or an account the node does not have: it has no headroom. That is never treated as unmetered.

Known limitation: the harness refreshes its token only while it runs. So an account with no agent running can be at fault once its token expires, which leaves it no headroom until an agent on that login runs again. Allowing work onto such an account would be a change to ADR-0035 point 3, not a change to this code.

## Seeing it

```
pao budget
```

lists each account with its state, the share left of each window and when it was read, and whether it has headroom by the default rule. An agent's own rule may be stricter. The same is served at `GET /v1/budget` on the local channel.
