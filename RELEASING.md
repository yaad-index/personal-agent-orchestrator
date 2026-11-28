# Releasing

pao is released with [release-please](https://github.com/googleapis/release-please),
from [Conventional Commits](https://www.conventionalcommits.org/). A release is a
git tag (`vMAJOR.MINOR.PATCH`), a GitHub Release, and its entries in
`CHANGELOG.md`.

## Pull request titles

The repository merges by squash only, and the squash commit's subject is the pull
request's title. That subject is what release-please reads, and it becomes the
changelog line as written, so **the title is the changelog entry**:

```
<type>(<scope>): <what changes, in the present tense>
```

- `feat`: a change in what pao does, visible to whoever runs it. Bumps the minor version.
- `fix`: a defect corrected. Bumps the patch version.
- `perf`, `revert`, `deps`: listed, and bump the patch version. `deps` is
  Dependabot's prefix for dependency updates.
- `docs`, `test`, `refactor`, `build`, `ci`, `chore`, `style`: not listed, no
  release. Use one of these for a change that alters nothing pao does when it
  runs, whatever files it touches. An ADR is `docs(adr)`.

These are set in `release-please-config.json`; any other type is dropped from
the changelog. The scope is the package or area the change is about (`comm`,
`config`, `ui`, …); a change spanning several takes the main one, or none. A breaking change takes a
`!` after the type or scope (`feat(config)!: …`). While the version is below 1.0.0
it bumps the minor version, not the major.

A squash merge leaves the commit body empty (`--body ""`), so footers such as
`BREAKING CHANGE:` or `Release-As:` in a pull request's description never reach
`main`; put what release-please must see in the title.

## Cutting a release

1. The `release-please` workflow keeps one release pull request open. It updates
   itself as commits land on `main`.
2. Review it by the commit range, not by its own diff, which is generated from
   that range: `git log v<last>..origin/main --format=%s` lists every subject
   since the last release, and each `feat`/`fix`/`perf`/`revert` should appear in
   the changelog and nothing else.
3. Merge it under the usual rule: two approvals and green CI. Merging it tags
   the release and publishes the GitHub Release.

Without a `RELEASE_PLEASE_TOKEN` secret, CI does not start on the release pull
request by itself; close and reopen it once to start it.

The first release is `0.1.0`. History before release-please was set up is not
in the changelog: it starts from the first conventional subject merged after it.
A release publishes no binaries; build pao from the tag.
