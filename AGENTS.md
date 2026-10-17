# impact-graph

`impact` is a Go CLI (and MCP server) that indexes a git repository into a symbol-level graph with tree-sitter and answers impact, trace, meet and review queries. See `README.md` for the model and `docs/design.md` for the design.

## Rule

Drive the project only with `devops agents <command>`. If `command -v devops` fails, stop and report that the runner image lacks the devops CLI; don't fall back to `./devops`, or to raw `go` commands for anything that has an entry below.

## Commands (`devops agents help` lists them)

- `bootstrap` – `go mod download` and `go build ./...` (~30 s cold). The session hook runs it for you.
- `build [pkg]` – `go build ./...`.
- `test [pkgs/flags]` – `go test ./... -cover` (~10 s). Example: `devops agents test ./internal/graph -run TestWalk`.
- `lint` – `go vet`, gofmt check and `golangci-lint` (v2, `.golangci.yml`), the same as CI (~15 s).
- `fmt` – `gofmt -w .`; run it if `lint` reports unformatted files.
- `impact <args>` – runs the CLI from source (`go run ./cmd/impact`), e.g. `devops agents impact index --root . -q` then `devops agents impact stats --root .`. It writes `.impact/` (gitignored).

## Waiting for the hook

`devops agents wait` blocks until bootstrap finishes (0 ready, 1 failed, 124 timeout); `devops agents status` returns at once (0 ready, 1 failed, 2 running, 3 not started). If it failed, `devops agents init --retry --trace` reruns it and logs every command.

## Conventions

- Conventional Commits (`feat:`, `fix(mcp):`, `ci:`), as in `git log`.
- Before pushing run `test` and `lint`: they are the CI checks.
- Keep `internal/graph` and `internal/community` pure Go; only `internal/extract` uses cgo.

## Gotchas

- The module builds with cgo (tree-sitter grammars are C), so a C compiler (`cc`) is required. There is no Docker stack, web UI or `*.test` URL.
- `impact index` and `impact review` read `git log` for co-change history; a shallow clone gives less history.
- No secrets are needed. Never decrypt `.gitsecret` or ask for GPG keys.
