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
  poll: 60s
```

- `metered` must be set on every account. `false` declares the account unmetered: its agents always have headroom, and it is never read.
- A metered account names its `reader` and the `credentials` file that reader reads. The `claude-subscription` reader reads the coding-agent harness's credentials file for the login. A relative path is resolved against the config file's directory.
- `budget.poll` is how often each metered account is read. The default is 60s.

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

A metered account whose figure cannot be read is at fault. That covers a refused token, an endpoint error, a window missing from the answer, or no reader for it in this build. Work already running on it keeps running. The account has no headroom until it reads again. The operator is told when an account enters a fault and when it leaves one.

The same rule covers an agent whose profile names no account, or an account the node does not have: it has no headroom. That is never treated as unmetered.

Known limitation: the harness refreshes its token only while it runs. So an account with no agent running can be at fault once its token expires, which leaves it no headroom until an agent on that login runs again. Allowing work onto such an account would be a change to ADR-0035 point 3, not a change to this code.

## Seeing it

```
pao budget
```

lists each account with its state, the share left of each window and when it was read, and whether it has headroom by the default rule. An agent's own rule may be stricter. The same is served at `GET /v1/budget` on the local channel.
