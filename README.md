# impact

Ask a repository what a change influences, and how one thing reaches another: from a tool that creates a booking to the
report that reads it, from a price an owner types to the limit that stops a session.

`impact` indexes a git repository into a **symbol-level graph** with tree-sitter (classes, methods, struct fields, database
columns, routes, MCP tools, event types) and answers four questions:

| Question | Command |
|---|---|
| What does a change here affect, and what can affect this? | `impact impact <node> [-d up] [--flow]` |
| How does a value get from A to B? | `impact trace <a> <b> [--via <node>] [--paths N]` |
| What do two things share (the columns, fields and events both touch)? | `impact meet <a> <b>` |
| What did my change leave out (tests, docs, callers, files that usually change with it, the companion changes the repository's rules demand)? | `impact review` |

It is a standalone Go module (`github.com/convidera/impact-graph`) with no dependency on the platform that uses it (convidera/managed-coding-service). The only repository-specific input is an
optional `impact.toml` next to the code it indexes.

```sh
impact index                       # build .impact/graph.db (about a second here)
impact find SpendGuard::assert     # locate nodes by id or name
impact impact column:spend_entries.amount_micros --flow -d up --only code
impact trace splitClaudeModelCost SpendGuard::assertAllowed --via column:spend_entries.amount_micros
impact meet mcp:create-timesheet-entry-tool mcp:analyse-monthly-timesheet-tool   # in a Laravel repo
impact review                      # the working tree against the branch's upstream; starts with unmet obligations and the changed areas
impact review --strict             # the same, and exit 1 when a declared obligation is not met (for CI and hooks)
impact suggest                     # candidate [[obligation]] tables mined from git history
impact overview                    # the repo map: repository, commit, counts, top communities, god nodes
impact communities --top 10        # the subsystems (Leiden communities); --id N shows one
impact hubs                        # god nodes: the most connected symbols and contracts (alias god-nodes)
impact merge a.db b.db --out m.db  # several repositories' snapshots into one graph
impact golden                      # the flows pinned in impact.toml
impact check                       # drift checks: hand-kept lists against the contracts that declare them
impact query repos                 # JSON for a graph browser: repositories and how they connect (also `nodes [text]`, `node <id>`)
impact mcp                         # the queries as an MCP server on stdio
```

Install a release binary (linux and macOS, amd64 and arm64; replace `vX.Y.Z` with a release tag, the asset names are `impact-<os>-<arch>`; `impact-linux-musl-<arch>` is a static binary for alpine; `checksums.txt` lists the sha256 of all):

```sh
curl -fsSL https://github.com/convidera/impact-graph/releases/download/vX.Y.Z/impact-linux-amd64 -o impact && chmod +x impact
impact version
```

Or build it: `go build -o impact ./cmd/impact` (needs a C compiler: the tree-sitter grammars are C, so this is the one module
that builds with cgo; a release is built natively per platform). Then `impact index --root <repo>` and the subcommands above.
`impact stats` prints the schema and engine version, the repository, the commit and the community count. Querying a stored
snapshot needs no cgo: `internal/graph` links a pure Go SQLite driver.
In convidera/managed-coding-service the same commands run as `devops agents impact <command>`.

## What it finds

**PHP and Laravel** (zero configuration):

- classes, methods, properties, traits, enums, interfaces; calls resolved through typed parameters, promoted constructor
  properties, `@var` / `@property` / `@return` docblocks, return types, `new`, `static`/`self`/`parent` and inheritance.
  `new Foo(...)` becomes a `creates` edge (the class it constructs) in addition to the `calls` edge to its constructor; a
  static call or a type hint stays `uses`
- the **database schema** from the migrations (`up()` only, applied in file order: create, add, drop, rename), each model's
  table (`$table` or the plural snake-case name), and for every Eloquent call the columns it **reads** (`where`, `orderBy`,
  `join`, `sum`, ... on a typed query), **writes** (`create`, `update`, `fill`, `insert`, `updateOrCreate`, a `new Model([...])`)
  and attributes it reads (`$entry->date`)
- routes (prefix groups, `apiResource`) and the controller method that handles each; MCP tools (named after their class, as
  Laravel MCP does) and the server that registers them; JSON resources tied to the model they present
- framework hooks that hide a call: `Feature::with()` that reflects the called class and runs `handle()`, `Resource::make()`
  running `toArray()`, `Job::dispatch()` running `handle()`, calls through an interface reaching every implementation

**Go**: packages and modules, types, struct fields (reads and writes, also through keyed literals and elided slice
elements), methods through embedded types, calls through interfaces to every type that has the interface's methods. A
composite literal of a named struct type (`Foo{...}`, `&Foo{...}`, including one elided inside a slice or map literal)
becomes a `creates` edge.

**TypeScript, JavaScript and Vue** (zero configuration): functions at any nesting depth (a composable and the handlers inside
it), classes, object-literal members, types, one symbol per module and per component. Calls are resolved through imports
(relative paths and the `@/` aliases of `tsconfig`, re-exports), `new`, `this`, typed parameters and what a function returns,
so `const { start } = useStartSession(); start()` and `useSessionApi().sessionsIndex()` reach the function that is called.
`new Foo(...)` becomes a `creates` edge (the class it constructs) in addition to the `calls` edge to its constructor.
Vue single-file components are parsed from their `<script>` blocks; the template links a component to the children it renders
and to the script functions its `@click`, `v-` and `{{ }}` expressions call. **HTTP calls** (`axios.post('v1/entries')`,
`fetch(url, {method})`, a generated client's `axiosInstance.get('/sessions')`) become `requests` edges to the backend route
with that verb and path, so a trace crosses the browser/server boundary: click handler → composable → API module → route →
controller → feature → column. A base-URL prefix the client supplies (`/api`, `/v1`) may be missing from the call.

**Python** (zero configuration): modules, classes, functions and methods; imports (absolute inside the repository and
relative, `from . import auth`, `from .api import call`), calls to imported functions and module attributes, `self.method()`,
`self.attr.method()` where `__init__` assigned the attribute a class, constructors (calling a class is a `creates` edge to it), and HTTP calls to routes
(`requests.get("https://host/api/x")`, f-strings).

**OpenAPI** (zero configuration): a JSON or YAML document with a top-level `openapi` or `swagger` key becomes `schema` and
`operation` nodes that point at their line in the document. An operation `uses` the schemas of its parameters, body and
responses (a schema `uses` the ones it refers to), `maps_to` the route that serves it (verb and path, a `servers` prefix such as
`/api` accounted for), and a schema `maps_to` the class, struct or type of the same name in every language: the PHP Data
class, the Go struct and the generated TypeScript interface that share it. Several types of one name in a language are told
apart by the document's name (`adapter-api.json` and `.../adapterapi/`), else joined as guesses (0.6). The path of an operation
is also a vocabulary value, so a function that names it as a string (a hand-written HTTP client, a router registration) is
linked to the operation: a change to a Data class then reaches `integration/api.json` in `impact review`, and the PHP client
method that calls an operation is one hop from the Go type that serves it. A document larger than `[index] max_file_bytes`
(default 1 MiB) is not read.

**Any language**: documents that name a thing in backticks or links (`documented_in`), tests found by convention, files that
change together in git history (`co_changed`), and string literals that match a declared vocabulary (event types, tool names),
now at function level in Go, PHP, TypeScript, Vue and Python.

**Across services** (`impact.toml`): a vocabulary (`[[vocabulary]]`) turns a list of shared string values into nodes. The Go
code that emits `usage_update` and the PHP `match` arm that handles it meet in `event:usage_update`, with the direction taken
from the producer and consumer globs. A value that is a bare word (`error`, `plan`) is also an ordinary string elsewhere, so a literal of it joins the vocabulary
only when the same function or file mentions another value of that vocabulary, or when it arrives through a constant or enum
case; a lone one is kept at confidence 0.65, below the walk threshold. The rule is lifted for a value whose entry in the
vocabulary file says `"ambiguous": false`, and for a vocabulary whose `produce` and `consume` globs cover at most five files.
A `[[link]]` with a written reason declares what no extractor can see, for example a
price that leaves the code in an agent's config file and comes back in its output.

**Deployment values** (`[[mount]]`): every key of a Helm values file (`values.yaml`, `values-stage.yaml`, `stage.values.yaml`) is a
`config` node, with no configuration. A `[[mount]]` in the repository that owns the Go types says that a values path is the
body of a struct, for example `agentEnvironments.*.spec` is an `AgentRunnerEnvironmentSpec`; the engine walks the struct by its
json tags and makes one pattern node per field path (`agentEnvironments.*.spec.adapter.resources`) linked to the Go field.
`impact merge` (and the index itself, for a values file of the same repository) joins each key that matches a pattern, so the
`adapter.resources` of a stage values file in a deployment repository, which has no `impact.toml`, answers "what code does this
value feed" and the Go field answers "which deployments set me" (`impact impact -d up`), with `file:line` on both sides.

## The model

A graph of nodes (`php:App\Foo::bar`, `go:<import path>.Type.Method`, `column:table.col`, `route:POST /x`, `mcp:tool-name`,
`event:name`, `file:path`) and edges with a **kind**, a **confidence** and the **evidence** (`file:line`) that produced them.
An edge is stored in its natural direction (a function `reads` a column, a caller `calls` a callee); each kind says how a
query walks it:

- **impact** (default): the direction a change spreads. A changed callee affects its callers; a changed column affects the
  functions that read it.
- **flow** (`--flow`, and `trace`/`meet`): the direction a value travels. Arguments go down into a callee, return values come
  back up, a function writes a column and another reads it.

Costs rank what a query finds. A data edge (call, read, write, event) costs 1; a structure edge (inheritance, class
membership) costs 3, because "this class extends that one" says little about the value. `--max-cost` bounds the walk and
`--min-confidence` (default 0.7) hides guesses such as a call on an untyped receiver that only one method name matches.

Two rules keep results honest:

- **Call and return must match.** A flow that descends into a shared helper may only come back out through the call it entered
  by, so two unrelated features that both call `authenticatedUser()` are not connected through it.
- **A `match` arm belongs to its value.** A dispatcher that handles `usage_update` and `turn_finished` consumes both, but a
  flow that entered through one event can only leave through that event's arm (or an unconditional call).

Hub nodes (more than `--hub-limit` edges) are reached but not expanded and are listed, so one giant model or command does not
flood a query.

Every edge also carries its **provenance**, the reason it exists: `ast` (syntax and declared types), `convention` (a framework
or naming convention: Eloquent columns, routes, tool names, URL matching, `Feature::with`), `contract` (a declared vocabulary),
`declared` (a `[[link]]` in `impact.toml`), `docs`, `history` (git co-change) and `guess` (a heuristic below 0.7 confidence).
Query output shows it next to the confidence (`[calls 0.95 ast]`), and `--json` includes it as `prov`. The values `ticket`,
`embedding`, `llm` and `human` are reserved for the knowledge layer described below.

Node types belong to one of three **layers**: `file` (file, test, doc), `code` (class, function, method, field), `contract`
(table, column, route, mcp-tool, value) and `knowledge` (requirement, ticket, decision, doc-section, feature). Nothing extracts
knowledge nodes yet; the edge kinds `implements`, `refines` and `derived_from` are reserved to connect them to code. A walk ends
at a knowledge node, as it ends at tests and docs, so a requirement that many functions implement does not link them to each
other, and results show such nodes in a `knowledge` group.

Every non-value node carries the `repo` it came from (`owner/name` from the git origin remote, or `[index] repo = "..."`), and
each snapshot records its `repo`, `commit`, `schema_version` (currently 2) and `engine_version` in its meta table. `Load`
refuses a snapshot of another schema version with an error that asks for `impact index` (a newer snapshot says it was written by
a newer engine), so a platform that stores snapshots can tell which ones to rebuild after an engine upgrade. `impact merge`
(and `graph.Merge`) combines the snapshots of several repositories into one graph: nodes of different repositories get distinct
ids (`<repo>//<id>`), while shared vocabulary values such as events and tool names keep one id, so repositories join through the
contracts they share.

## Orientation: communities and god nodes

After every `impact index` the symbols and contracts are clustered into **communities** (subsystems) with the Leiden algorithm,
a pure Go implementation that is deterministic for a seed. Edge weights depend on the kind (1 for calls, reads, writes,
requests and similar; 1.5 for `maps_to`; 3 for `member_of`; 0.5 for `uses`, `creates`, `registers` and `mentions`) times the edge
confidence; docs and git history do not count, and edges below 0.5 are ignored. Hubs (weighted degree above the 99th
percentile) are left out of the clustering and assigned afterwards to the community they connect to most, and so are test
symbols. A community with fewer than `min_size` members (default 5) is dissolved: each of its members moves to the kept
community it connects to most, and a member with no connection to a kept community stays out of all of them. Files, docs
and test files have no community. Each community has a label built without an LLM (the most common directory of its code
members, or of all its members when it has no code, plus its two best connected symbols or tables), a size and a cohesion
(the share of its members' edge weight that stays inside).

`impact overview` is the repository map to read first on an unfamiliar repository: the repository, commit, counts per layer,
the top communities, the god nodes and the contract counts. `impact communities [--top N] [--id N]` lists or shows communities;
`--recluster --resolution X --hub-percentile P` recomputes them in memory to tune the result and never writes the snapshot.
`impact hubs` (alias `god-nodes`) lists the **god nodes**, the most connected symbols and contracts, by degree (distinct
code and contract neighbours, `member_of` excluded), with in and out counts and the number of other communities each one
bridges. They are where a change floods. Generated code is never a god node (see below), but it still counts as a neighbour.

```toml
[cluster]
resolution = 1.0          # higher gives smaller communities
hub_percentile = 99       # nodes above this weighted-degree percentile are assigned afterwards
min_size = 5              # default; communities below this size are dissolved into their neighbours
seed = 1                  # the same seed gives the same communities
disabled = false          # true skips clustering
```

Modularity has a resolution limit: on large graphs neighbouring groups merge into one community, so raise `resolution` to get
smaller communities.

## Configuration: `impact.toml`

Optional. See the one in the repository root for a worked example.

```toml
[index]
exclude = ["frontend/src/api/generated/**"]

[[vocabulary]]                       # a shared list of values becomes nodes
name = "session-event"
type = "event"                       # node ids are event:<value>
file = "contracts/session-events.json"
list = "x-events"
key = "name"
produce = ["session-runtime/**"]     # who emits
consume = ["backend/**"]             # who handles

[[link]]                             # a connection no extractor can see, with its reason
from = "OpencodeProfile::opencodeCost"
to = "backend.opencodeStore.Children"
why = "the prices go into the agent's config; the adapter reads the priced cost back from its store"

[[mount]]                            # a part of a values file is the body of a Go struct
path = "agentEnvironments.*.spec"    # key path; * is any one key, [] a list element
go_type = "AgentRunnerEnvironmentSpec"   # struct name, or its full key when the name is not unique
reason = "the chart renders it into the spec of an AgentRunnerEnvironment"

[[golden]]                           # a flow that must keep existing; `impact golden` fails otherwise
name = "a reported cost reaches the limit check"
from = "backend.splitClaudeModelCost"
via = ["column:spend_entries.amount_micros"]
to = "SpendGuard::assertAllowed"
```

### Obligations

`impact review` lists what a change relates to; an obligation is a rule a repository states about *companion* changes: "when
these files change, one of those must change too". The rules that live as prose in an `AGENTS.md` or a pull request template
(an API change regenerates the client, a UI change updates its test plan) are declared once, and `review` (CLI and the
`impact_review` MCP tool) reports the ones a change triggers and does not satisfy, with the files that triggered them, the
reason and the command that fixes it. `impact review --strict` exits 1 for CI.

```toml
[[obligation]]
name = "API shape: regenerate the OpenAPI document"
when = ["backend/app/Domain/*/Data/**", "backend/routes/**"]   # a changed file matches one of these ...
unless = ["**/*Test.php"]                                      # ... and none of these (optional)
require = ["integration/api.json"]                             # then some changed file must match one of these
why = "the document and the generated client derive from the Data classes and routes"
fix = "devops agents generate"                                 # optional
```

`impact suggest` mines the git history for candidates (`N of M commits that touched app/Data/** also touched
integration/api.json`) and prints them as `[[obligation]]` tables to confirm. A companion that changes in most commits is not
proposed, and neither is a generated file as a trigger. It proposes; a person decides.

### Generated code

`[index] generated` lists globs of generated files (default: `**/generated/**`, `**/__generated__/**`, `**/*.generated.*`,
`**/*_generated.go`, `**/zz_generated*.go`, `**/*.pb.go`; the globs you give add to these). Generated files are indexed like
code, so a call into them stays an edge and they take part in clustering and in walks. Their file nodes are classified
`generated` and are not listed as results, like code files, and `impact hubs` (god nodes) never picks a generated symbol as
a candidate.

### Drift checks

`impact check` compares lists that people keep by hand with the contracts that declare them: a PHP constant, a TypeScript array,
Go constants, a docs table, the keys an API client sends. Each `[[check]]` names the files to read, a pattern that extracts the
values (group 1) and the source of truth, and fails when the two disagree. It reads text and JSON, not the graph, so it works for
any language.

```toml
[[check]]
name = "sse-named-events (backend)"
vocabulary = "session-event"          # the values of a [[vocabulary]], optionally filtered: where = "named"
files = ["backend/app/Domain/Events/Services/SessionEventFrame.php"]
region = ["NAMED_EVENTS = \\[", "\\];"]   # narrow the text between two patterns
extract = "'([a-z_]+)'"
mode = "equal"                        # equal | subset (default: no undeclared value) | superset (every declared value is found)

[[check]]
name = "CRD: spec keys PHP writes are AgentSessionSpec fields"
source = "go-struct:session-runtime/api/v1alpha1/agentsession_types.go#AgentSessionSpec"   # also: openapi-schema, openapi-paths, ...
files = ["backend/app/Domain/Runtime/Services/HttpAgentSessionRuntime.php"]
region = ["'spec' => \\[\\s*\\n", "\\n            \\],"]
extract = "'(\\w+)' =>"
```

Sources are `openapi-schema:<file>#<Schema>`, `openapi-paths:<file>`, `openapi-operations:<file>`, `go-struct:<file>#<Type>`
(json names, read with the Go parser, embedded `json:",inline"` structs followed), `go-struct-required:<file>#<Type>` (those
without `omitempty`) and `json:<file>#<selector>` for everything else in a JSON document; a list unions them. A selector is
dot-separated keys, each with one optional suffix: `[]` (the elements of an array), `[k=v]` (the elements whose `k` equals `v`)
or `{}` (the property names of an object). Strings reached are the values:

| Selector | Values |
|---|---|
| `x-vocabularies.usage[]` | the strings of that array |
| `x-agents[].kind` | the `kind` of every element |
| `x-events[producer=mcs].payload.properties{}` | the payload keys of the events of one producer |
| `x-event-types[].type` | the `type` of each element |

`found_source` reads the found side from a document instead of code. (The platform-specific kinds `registry-*`,
`openapi-event-types` and `openapi-extension` of earlier versions are gone: each is one `json:` selector.)

Node arguments accept an exact id, a repository path or any unique substring (`SpendGuard::assertAllowed`); an ambiguous one
prints the candidates instead of guessing.

## Structured queries for a graph browser

`impact query repos`, `impact query nodes [text] [--repo r] [--type t] [--limit n]`, `impact query node <id>` and `impact query lookup <id>...` (nodes by id, with their degree) print JSON: the
repositories of a snapshot with commit, build time, node and edge counts per type and how they connect (`shared_values`,
`direct_edges`), a search over node labels and ids ranked by degree, and one node with up to 60 neighbours per direction
(kind, confidence, provenance, `file:line`). The types are in `internal/query` and are the contract: a service that shows the
graph reads this output (with `impact impact --json` and `impact trace --json` for walks) instead of the snapshot's tables,
which are an implementation detail and change with the schema version. A command whose node or repository argument names nothing in the graph (no match, ambiguous, unknown) exits with status 2 and the reason on stderr; any other failure exits 1.

## MCP server

`impact mcp` serves `impact_find`, `impact_impact`, `impact_trace`, `impact_meet`, `impact_review`, `impact_reindex`,
`impact_overview`, `impact_community` and `impact_god_nodes` over newline-delimited JSON-RPC on stdio. An agent calls `impact_impact` while it plans a change and `impact_review` before it
finishes. See [docs/design.md](docs/design.md) for how this becomes a platform feature.

## Several repositories

`impact merge a.db b.db --out m.db` combines the snapshots of several repositories into one graph. Each node keeps its
repository (ids become `owner/name//<id>`), and the repositories meet only where they share a value: an event type, a tool
name, a `[[mount]]` that matches a values key. A query that should stay in one repository says so:

```sh
impact overview --repo acme/api           # the map of one repository of the merge
impact find Order --repo api              # a unique name without the owner also works
impact hubs --repo acme/api
impact communities --repo acme/api
```

The MCP tools `impact_find`, `impact_overview`, `impact_god_nodes` and `impact_community` take the same `repository`
argument. `impact mcp --readonly --db merged.db --checkout owner/name=/path/to/checkout` (repeatable) also offers
`impact_review` for those repositories: it diffs the working tree of the repository named in `repository` against its base,
reads that repository's `impact.toml` for obligations, and matches the changed files to the nodes of that repository only,
while the graph stays the snapshot (the code as it was indexed). Items found in another repository are labelled with it.
`impact_reindex` is never offered on a merged snapshot. On a snapshot of one repository the argument is accepted and ignored. Shared values are left out of the lists and
counted apart.

`impact overview` on a merge starts with one line per repository (nodes, edges, commit) and then says how the repositories
connect: `cross-repository edges` counts the edges straight from one repository's own nodes to another's, and
`connected through shared values` the shared values that reach two or more of them. Two repositories that connect to
nothing get a note; with several repositories, the pairs that do not connect are listed. `impact merge` prints the same
block to stderr. `impact find` and `impact_find` say how many hits each repository contributed.

Repositories also join without any configuration, through names that are unique across projects:

- **environment variables**: a `.env.example` (or any `.env`, `*.env`), a compose `environment:`, a Kubernetes or Helm `env:` list
  defines `ADAPTER_CLONE_TIMEOUT`; a PHP `env()` or `getenv()`, a Go `os.Getenv`/`os.LookupEnv`, a TypeScript
  `process.env.X` or `import.meta.env.X` and a Python `os.environ`/`os.getenv` reads it;
- **images**: a workflow pushes `ghcr.io/org/app-backend:latest` (`docker push`, `docker build -t`, the `images:` and `tags:`
  of a docker action) and a manifest deploys `registry.example.com/org/app-backend:1.2`; the registry, the tag and the digest
  are dropped, so both name `org/app-backend`.

A name joins repositories only when one side produces it and another side consumes it. Producers are the deployment
definitions (compose `environment:`, Kubernetes and Helm `env:` lists, workflow pushes); consumers are the code that reads a
variable, a dotenv file that expects it, and the manifests that deploy an image. Two repositories that only read the same
variable, or only deploy the same image, are not connected: each keeps its own copy of the name.

Generic names (`PORT`, `DB_*`, `APP_*`, ...) and images without an owner (`postgres`, `nginx`) never join; the lists are in
`internal/extract/joins.go`. The edges of these two kinds have confidence 0.7, the default walk threshold, so walks follow
them without a flag. Event types, tool names and `[[mount]]` links join as they did. Routes,
tables and columns never join by their plain names: two services that both have a `users` table stay apart.

## Limits

- Resolution is by declared types, docblocks and conventions, not a type checker. An untyped receiver produces no edge (or a
  low-confidence guess for a distinctive method name), which is deliberate: a wrong edge costs more than a missing one.
- Not extracted yet: OpenAPI documents (a client is joined to a route by URL, not by operation id), Go HTTP routes, validation
  rules, Helm templates (values files are read, see `[[mount]]`; a mount follows structs declared in the
  repository, a field of an imported type such as `corev1.ResourceRequirements` ends the pattern at that field), JSON payload keys as nodes, and knowledge nodes (requirements, tickets,
  decisions, documentation sections, features).
- Client side, resolution is structural, not a type checker: a call through a value the extractor cannot follow (a Vuex
  `dispatch('module/action')` by string, an event bus, a prop callback, a Python attribute whose class is not visible) produces
  no edge. Vue templates are read with patterns, not a template parser.
- Go interface dispatch is by method-name sets, an over-approximation of structural typing.
- The index is rebuilt in full (about 1 s for this repository, 14 s for the 7,000 files of the Go standard library); an
  incremental index keyed by file hash is the next step.

## Develop

```sh
./devops install   # once: downloads the devops CLI (https://github.com/convidera/devops-cli) to ~/.local/bin
./devops test      # go test, builds Laravel and Go fixture repositories in temp directories
./devops lint      # go vet, gofmt check and golangci-lint, as in CI
./devops fmt       # gofmt -w .
./devops build     # go build ./...
./devops impact index --root .   # run the CLI from source
```

Coding agents use `devops agents <command>` instead; see `AGENTS.md`.

`internal/graph` (model, walk, SQLite store, merge) and `internal/community` (Leiden clustering) are pure Go. `internal/extract` is the only package that needs cgo: one file per
language (`php_decl.go`, `php_body.go`, `golang.go`), one for the Laravel pack (`laravel.go`), and the cross-cutting extractors
(`vocab.go`, `docs.go`, `history.go`, `links.go`). `alloc.go` points the parser at the C library allocator, which halves the
index time compared with the bindings' default.
