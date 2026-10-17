# Design: a repository knowledge graph for agents

Status: the engine is built (this directory). The platform integration (repository catalog, indexer job, snapshot storage, MCP
tools served by MCS, UI) is planned. Decision D25 in [docs/architecture/decisions.md](https://github.com/convidera/managed-coding-service/blob/main/docs/architecture/decisions.md).

## Goal

Agents plan and review changes and forget what is related: the test that exercises the code, the document that names it, the
other service that reads the same column, the changelog entry and the generated client that always change with it. The graph
answers "what else is connected to this?" for any git repository, as a tool an agent calls while it plans and again before it
finishes.

Three concepts, in this order of maturity:

1. **Facts**: the graph. Symbols, calls, reads and writes of columns and fields, routes, tools, shared string values, each with
   a confidence and its evidence. Built.
2. **Review**: given a diff, what is related and absent from it. Built (`impact review`, tests, documents, contracts, callers,
   files that changed together in git history).
3. **Obligations**: "if A changes, B must change too" (a `Data` class means regenerate the client, a UI change means a test
   plan). Today they live as prose in AGENTS.md. Planned: declared in `impact.toml`, mined from history (already visible as
   `co_changed`), or proposed from AGENTS.md and confirmed by a human.

## Technical decisions

**Go and tree-sitter, not a regex indexer.** The Python tool this replaced (`tools/impact-graph`, removed) worked at file
level with regular expressions and hand-written configuration (607 lines for this repository). A parser makes function and method the unit of
the graph, which is what makes a trace precise: a file that imports a model says nothing about the value, a method that calls
`Entry::create(['day' => ...])` does. tree-sitter gives error-tolerant, fast, multi-language syntax trees; it does not resolve
names, so resolution (typed parameters, promoted properties, docblocks, imports, inheritance, interface method sets) is ours.
Go because the platform's runtime is Go, a stored snapshot can be queried by a pure Go service, and indexing parallelises.

**cgo only where it has to be.** The grammars are C. The `extract` package needs cgo, `graph` does not (pure Go SQLite via
`modernc.org/sqlite`, already a dependency of `session-runtime`). The module is the one place here that builds with cgo; its
CI job needs a C compiler. A pure Go runtime (wasm or a port) would remove that and was not evaluated. The Go bindings route
every allocation of the C parser through a Go callback; `alloc.go` replaces that with libc `malloc` and halves the time.

**No graph database.** Graphs are small (about 20k edges for this repository, 320k for the Go standard library) and queries
are bounded best-first walks with a custom cost, which Cypher does not improve. A snapshot is one immutable SQLite file per
repository and commit: cacheable, diffable, loadable by an agent pod or a service. Revisit it for cross-organisation graphs
beyond roughly ten million edges, or for ad-hoc graph queries by people.

**Two walking modes.** Edges are stored in their natural direction and each kind says how it is walked. *Impact* follows the
direction a change spreads (a changed callee affects its callers). *Flow* follows the data (arguments down, return values
up, writer to column to reader). Mixing them gives wrong answers either way.

**Call/return matching and match-arm tags.** Without them a shared helper connects everything to everything, and a dispatcher
that handles ten events connects each event to every handler. Both rules are cheap and remove most false paths (see the
tests in `internal/graph` and `internal/build`).

**Join only on globally unique identifiers, and only where data flows.** Repositories meet through a shared name only when
that name cannot mean something else in another project (an event type, a tool name, an environment variable such as
`ADAPTER_CLONE_TIMEOUT`, an image repository such as `org/app-backend`) and only when one repository produces it and another
consumes it. A name that two services merely read (`MEMCACHED_HOST` in every Laravel app) or merely set in their own compose
files is not a connection: `impact merge` splits such a node into one copy per repository (`internal/graph/joint.go`). A route,
a table or a column is never joined by its plain name, because `users` or `GET /api/users` exists in many services for
unrelated reasons: a join on it would be a guess that a query cannot tell from a fact. Names that every project uses (`PORT`,
`APP_*`) and images without an owner are left out by a list (`internal/extract/joins.go`).

**Zero configuration first, declared contracts second.** Everything an agent needs from a Laravel or Go repository comes from
the code. What crosses a process boundary (an event type, a price written into another program's config) needs one declaration
with a written reason, and a golden flow that fails when it breaks.

## Platform integration (planned)

Detailed in [docs/architecture/impact-integration.md](../../docs/architecture/impact-integration.md). In short:

- **A fixed step of the checkout.** Every repository that enters the workspace (initial checkout, `add_repository`, a project's
  defaults, a resumed pod) goes through `Cloner.Clone` of the adapter; the index runs right after it, as a child process, on the
  pristine checkout, and a failed index never fails the session.
- **Served in the pod.** The adapter offers the agent a second MCP server, `impact`, on loopback next to `mcs`; it works on the
  working tree, so `impact_review` sees uncommitted work, and needs no credential and no network.
- **Persisted by MCS, later.** The adapter uploads the snapshot keyed by repository and commit (object store plus a Postgres
  catalog) for status in the UI, reuse across sessions, a project-wide graph and reviews without a pod. Every snapshot carries
  its `schema_version` and `engine_version`, so the catalog can mark the snapshots that an engine upgrade makes stale, and
  each node its `repo`: a project graph is a merge of per-repository snapshots, with repository-namespaced ids and shared
  vocabulary values joined.
- **Provenance and layers, built; requirements, planned.** Every edge says why it exists (`prov`: ast, convention, contract,
  declared, docs, history, guess), and nodes sit in the file, code, contract and knowledge layers. The knowledge layer is
  reserved for requirements. Planned, in this order: links from tickets, sessions started from a ticket and the pull requests
  they produce, ticket numbers in commit messages and documents, and only then embeddings. A walk ends at a knowledge node, so a
  requirement does not connect the functions that implement it.
- **No graph database.** The walks are stateful (call/return matching, tagged arms, waypoints), the data is one repository
  (91 ms to load 23k edges), the unit is an immutable snapshot per commit and access follows repository access. A graph
  database can be a derived read model for organisation-wide questions later; `impact export` keeps that door open.
- **Security and quality.** A snapshot holds paths, names and `file:line`, not source bodies; it is reused only inside one
  project. After a pull request merges, compare the "related but untouched" prediction with follow-up commits and review
  comments to measure precision and recall per repository, calibrate confidences and hide a graph that is too noisy.

## What is verified

- `go test ./...` builds Laravel and Go fixture repositories and asserts the schema (a `down()` must not drop a table), column
  reads and writes, the reflective factory, routes, tools, vocabulary joins, match-arm pruning, interface dispatch, review and
  the MCP protocol.
- `impact golden` on this repository: a cost split by the Go runtime reaches the spend-limit check through the event, the
  ingestor, the ledger column and the guard; the owner's model price reaches the ledger through the agent; a limit set in the
  dashboard reaches the guard.
- On `convidera/t-rax2` with no configuration: the booking created by the MCP tool `create-timesheet-entry-tool` reaches
  `analyse-monthly-timesheet-tool` through `timesheet_entries.date`, `timesheet_id`, `duration`, `location`, `output`,
  `source` and `timesheets.user_id` and `month` (`impact meet`).
- The Go standard library (7,181 files, 113k nodes, 321k edges, 15 s) indexes without a crash.

## Not built, in order

1. Incremental index by blob hash.
2. OpenAPI operations and schemas as nodes so a request field joins its column, JSON payload keys of events as nodes, Vuex and
   event-bus dispatch by string. (TypeScript, Vue and Python symbols, and the HTTP join from a client to a route, are built.)
3. Stack detection that proposes an `impact.toml`; obligations; an onboarding agent session that opens that pull request.
4. The platform integration above.
5. The knowledge layer: requirement, ticket, decision, documentation section and feature nodes, linked to code by `implements`,
   `refines` and `derived_from` in the order given in the platform section, and SCIP indexes where a language has a good one,
   as an optional precision upgrade.

Built since: Helm values files as `config` nodes and `[[mount]]` (a values path is the body of a Go struct, joined across
repositories at merge).

Built since the last revision: Leiden communities with their labels and cohesion, god nodes (`impact hubs`), `overview`,
`communities` and `merge`, the snapshot schema version, repository-namespaced ids and merge, and edge provenance.
