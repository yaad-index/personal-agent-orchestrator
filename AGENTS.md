# AGENTS.md

Orientation for agents and contributors working **on** pao. To understand what
pao is, read the [README](README.md). The design lives in the architecture
decision records under [`adr/`](adr/README.md); read the ADR that governs an
area before changing it.

## The shape

One node daemon per machine routes input to coding-agent harnesses. The agent
is driven terminal-in (a paste through the terminal driver) and transcript-out
(the harness's own session transcript, never scraped from the terminal). The
core depends on nothing concrete, so the backend, the terminal driver, the
stores and the human channel stay swappable (ADR-0002).

## Package layout

```
cmd/pao/        thin CLI (Kong): parse flags, wire collaborators, run
internal/core/  the engine: domain types and the collaborator interfaces
internal/...    one package per concern, added as each ADR is built
adr/            architecture decision records
```

Behaviour lives in `internal/<concern>`; `cmd/pao` only parses and wires.

## Conventions

- Go 1.26, standard library first. A new load-bearing dependency is called out
  in the PR that adds it.
- Tests use testify (`assert` / `require`). `go test -race ./...` is the
  default run.
- Lint with golangci-lint v2.11.4 (the version CI pins); format with gofmt.
- pao's own files use generic, pao-namespaced names (`.pao.*`), never a
  backend's name (ADR-0002, ADR-0011).
- Name only the coding-agent harness and the terminal driver in code, comments
  and docs. Everything else is described by its role.
- ADRs are immutable once Accepted: a change is a new ADR that supersedes it.
  Detail the ADRs leave open is decided in code and captured afterwards.
- Code PRs merge on two reviews and green CI. An ADR change also needs the
  operator's approval.
- A PR's title is a conventional-commit subject (`feat(comm): …`,
  `fix(config): …`, `docs(adr): …`): it becomes the squash commit's subject,
  which release-please reads and puts in the changelog as written
  (RELEASING.md).

## The mesh wire

The wire between nodes is defined in `proto/pao/mesh/v1/mesh.proto`; the Go code
generated from it is committed under `internal/mesh/meshpb` and must not be edited
by hand. After changing the `.proto`, run `go generate ./internal/mesh/meshpb`. That
needs `protoc` and two plugins at the versions `scripts/genproto.sh` pins (the script
refuses any other); CI regenerates the code and fails if the committed files differ.

## The management UI

The UI's source is in `ui/` (Svelte 5, Vite, TypeScript, versions pinned in
`ui/package.json` and its lock file). Its build is committed under
`internal/webui/dist`, embedded in the binary and served at `/ui`, so building pao
needs no Node. After changing `ui/`, run `npm ci && npm run check && npm run build`
in `ui/` and commit the new `dist`. CI builds it again and fails if the committed
files differ. The page's content security policy allows nothing inline: no inline
scripts, no `style` attributes and no runtime-injected styles.

## Checks before a PR

```
go build ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run && go test -race ./...
```
