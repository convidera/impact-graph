package build_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/build"
	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/review"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func index(t *testing.T, root string) *graph.Graph {
	t.Helper()
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	g, st, err := build.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range st.Warnings {
		t.Logf("warning: %s", w)
	}
	return g
}

func hasEdge(g *graph.Graph, src, dst, kind string) bool {
	for _, e := range g.Edges {
		if e.Src == src && e.Dst == dst && e.Kind == kind {
			return true
		}
	}
	return false
}

func mustTrace(t *testing.T, g *graph.Graph, from, to string, via ...string) []*graph.Hit {
	t.Helper()
	o := graph.DefaultOptions()
	o.Mode, o.MaxCost = graph.ModeFlow, 30
	r := g.TraceN(from, via, to, o, 1)
	if len(r) == 0 {
		t.Fatalf("no flow path from %s to %s", from, to)
	}
	return r[0]
}

const laravelMigration = `<?php
use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration {
    public function up(): void
    {
        Schema::create('entries', function (Blueprint $table) {
            $table->uuid('id')->primary();
            $table->date('day');
            $table->decimal('hours');
            $table->timestamps();
        });
        Schema::table('entries', function (Blueprint $table) {
            $table->string('note')->nullable();
            $table->dropColumn('hours_old');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('entries');
    }
};
`

func laravelFixture() map[string]string {
	return map[string]string{
		"database/migrations/2026_01_01_000000_create_entries_table.php": laravelMigration,
		"app/Entry.php": `<?php
namespace App;
use Illuminate\Database\Eloquent\Model;
class Entry extends Model {
    protected $fillable = ['day', 'hours', 'note'];
}`,
		"app/Features/Feature.php": `<?php
namespace App\Features;
abstract class Feature {
    abstract public function handle();
    public static function with(...$parameters) {
        return (new \ReflectionClass(get_called_class()))->newInstanceArgs($parameters)->handle();
    }
}`,
		"app/Features/CreateEntry.php": `<?php
namespace App\Features;
use App\Entry;
class CreateEntry extends Feature {
    public function __construct(private $day, private $hours) {}
    public function handle() {
        return Entry::create(['day' => $this->day, 'hours' => $this->hours]);
    }
}`,
		"app/Features/UpdateEntry.php": `<?php
namespace App\Features;
use App\Entry;
class UpdateEntry extends Feature {
    private $entry;
    private $hours;
    public function __construct(Entry $entry, $hours) {
        $this->entry = $entry;
        $this->hours = $hours;
    }
    public function handle() {
        $this->entry->update(['hours' => $this->hours]);
        return $this->entry;
    }
}`,
		"app/Mcp/Tools/CreateEntryTool.php": `<?php
namespace App\Mcp\Tools;
use App\Features\CreateEntry;
use Laravel\Mcp\Server\Tool;
class CreateEntryTool extends Tool {
    public function handle($request) { return CreateEntry::with($request->day, $request->hours); }
}`,
		"app/Mcp/Tools/MonthlyReportTool.php": `<?php
namespace App\Mcp\Tools;
use App\Entry;
use Laravel\Mcp\Server\Tool;
class MonthlyReportTool extends Tool {
    public function handle($request) {
        return Entry::query()->where('note', 'x')->orderBy('day')->get();
    }
}`,
		"app/Mcp/Servers/AppServer.php": `<?php
namespace App\Mcp\Servers;
use App\Mcp\Tools\CreateEntryTool;
use App\Mcp\Tools\MonthlyReportTool;
use Laravel\Mcp\Server;
class AppServer extends Server {
    protected array $tools = [CreateEntryTool::class, MonthlyReportTool::class];
}`,
		"app/Http/Controllers/EntryController.php": `<?php
namespace App\Http\Controllers;
use App\Features\CreateEntry;
use App\Entry;
class EntryController {
    public function store() { return CreateEntry::with(1, 2); }
    public function index() { return Entry::query()->where('note', 'x')->get(); }
}`,
		"routes/api.php": `<?php
use App\Http\Controllers\EntryController;
use Illuminate\Support\Facades\Route;
Route::prefix('v1')->group(function () {
    Route::post('entries', [EntryController::class, 'store'])->name('entries.store');
    Route::get('entries', [EntryController::class, 'index'])->name('entries.index');
});`,
		"client/app.py": `from . import api
from .tools import call_tool

def run():
    api.fetch()
    return call_tool("monthly-report-tool")
`,
		"client/api.py": `import requests

def fetch():
    return requests.get("https://host.example/api/v1/entries")
`,
		"client/tools.py": `def call_tool(name):
    return name
`,
		"client/__init__.py": "",
		"tsconfig.json": `{
  "compilerOptions": { /* paths */ "baseUrl": ".", "paths": { "@/*": ["resources/js/*"] } }
}`,
		"resources/js/api/entries.ts": `import axios from 'axios';
export default {
  create(data, callback) {
    axios.post('v1/entries', data).then(callback);
  },
  list(month) {
    return axios.get(` + "`v1/entries?month=${month}`" + `);
  },
};
`,
		"resources/js/api/generated.ts": `import axios from 'axios';
export const getApi = (axiosInstance) => {
  const entriesIndex = () => axiosInstance.get('/v1/entries');
  return { entriesIndex };
};
export const useApi = () => getApi(axios);
`,
		"resources/js/store/booking.ts": `import entryApi from '@/api/entries';
import { useApi } from '@/api/generated';
import { callTool } from './rpc';

export function useBooking() {
  async function book(data: object) {
    return entryApi.create(data, () => {});
  }
  function load() {
    return useApi().entriesIndex();
  }
  function report() {
    return callTool('monthly-report-tool');
  }
  return { book, load, report };
}
`,
		"resources/js/store/rpc.ts": `export function callTool(name: string) { return name; }
`,
		"resources/js/components/Booking.vue": `<template>
  <div>
    <button @click="save">Save</button>
    <ChildCard />
  </div>
</template>

<script setup lang="ts">
import { useBooking } from '@/store/booking';
import ChildCard from './ChildCard.vue';

const { book } = useBooking();

function save() {
  book({});
}
</script>
`,
		"resources/js/components/ChildCard.vue": `<template><div>child</div></template>
<script setup lang="ts"></script>
`,
		"tests/CreateEntryTest.php": `<?php
namespace Tests;
use App\Features\CreateEntry;
class CreateEntryTest {
    public function test_creates() { CreateEntry::with(1, 2); }
}`,
		"docs/entries.md": "Entries are created by `App\\Features\\CreateEntry` and stored in `entries`.\n",
	}
}

func TestLaravelSchemaAndFlow(t *testing.T) {
	root := t.TempDir()
	write(t, root, laravelFixture())
	g := index(t, root)

	for _, id := range []string{"table:entries", "column:entries.day", "column:entries.hours", "column:entries.note", "column:entries.created_at", "column:entries.id"} {
		if !g.Has(id) {
			t.Errorf("schema is missing %s (down() must not drop the table)", id)
		}
	}
	if g.Has("column:entries.hours_old") {
		t.Errorf("a column that was never created must not exist")
	}
	if !g.Has("mcp:create-entry-tool") || !g.Has("mcp:monthly-report-tool") {
		t.Fatalf("MCP tools are named after their class")
	}
	create := `php:App\Features\CreateEntry::handle`
	if !hasEdge(g, create, "column:entries.day", graph.Writes) {
		t.Errorf("Entry::create([...]) must write the keys it sets")
	}
	if !hasEdge(g, `php:App\Mcp\Tools\MonthlyReportTool::handle`, "column:entries.note", graph.Reads) ||
		!hasEdge(g, `php:App\Mcp\Tools\MonthlyReportTool::handle`, "column:entries.day", graph.Reads) {
		t.Errorf("where() and orderBy() on a model query read their columns")
	}
	// an untyped property takes its type from the constructor parameter it is assigned from
	if !hasEdge(g, `php:App\Features\UpdateEntry::handle`, "column:entries.hours", graph.Writes) {
		t.Errorf("$this->entry->update([...]) must write columns when the constructor types $this->entry")
	}
	// the reflective factory: CreateEntry::with() runs CreateEntry::handle
	if !hasEdge(g, `php:App\Mcp\Tools\CreateEntryTool::handle`, create, graph.Calls) {
		t.Errorf("Feature::with must resolve to the called class's handle")
	}
	// the question of the t-rax2 use case: how does a created entry reach the report tool?
	p := mustTrace(t, g, "mcp:create-entry-tool", "mcp:monthly-report-tool")
	var ids []string
	for _, h := range p {
		ids = append(ids, h.ID)
	}
	want := []string{"mcp:create-entry-tool", `php:App\Mcp\Tools\CreateEntryTool::handle`, create, "column:entries.day", `php:App\Mcp\Tools\MonthlyReportTool::handle`, "mcp:monthly-report-tool"}
	if strings.Join(ids, "|") != strings.Join(want, "|") {
		t.Errorf("path\n got %v\nwant %v", ids, want)
	}
	// routes: prefix group, handler
	if !g.Has("route:POST /api/v1/entries") || !hasEdge(g, `php:App\Http\Controllers\EntryController::store`, "route:POST /api/v1/entries", graph.Exposes) {
		t.Errorf("route with prefix and handler expected")
	}
	// the server registers its tools; a Python client that mentions the tool name is joined to it
	if !hasEdge(g, `php:App\Mcp\Servers\AppServer`, `php:App\Mcp\Tools\MonthlyReportTool`, graph.Registers) {
		t.Errorf("a $tools list registers classes")
	}
	if !hasEdge(g, "py:client/app.py#run", "mcp:monthly-report-tool", graph.Consumes) {
		t.Errorf("a client that names a tool consumes it")
	}
	// docs and tests attach to what they name
	if !hasEdge(g, `php:App\Features\CreateEntry`, "file:docs/entries.md", graph.DocumentedIn) {
		t.Errorf("a document that names the class in backticks documents it")
	}
	hits, _ := g.Walk(create, graph.DefaultOptions())
	foundTest := false
	for id := range hits {
		if strings.Contains(id, "CreateEntryTest") {
			foundTest = true
		}
	}
	if !foundTest {
		t.Errorf("a change to CreateEntry::handle must reach its test")
	}
}

func goFixture() map[string]string {
	return map[string]string{
		"impact.toml": `
[[vocabulary]]
name = "ev"
type = "event"
values = ["usage_update", "turn_finished"]
produce = ["runtime/**"]
consume = ["backend/**"]

[[golden]]
name = "runtime cost reaches the ledger"
from = "Store.Children"
to = "Ledger::record"

[[link]]
from = "Ledger::record"
to = "event:turn_finished"
why = "test link"
`,
		"runtime/go.mod": "module example.com/runtime\n\ngo 1.22\n",
		"runtime/usage/usage.go": `package usage

type Child struct {
	Cost  float64
	Model string
}

type Provider interface {
	Children() []Child
}

type Reporter struct {
	p Provider
}

func New(p Provider) *Reporter { return &Reporter{p: p} }

func (r *Reporter) Report() map[string]any {
	cs := r.p.Children()
	c := cs[0]
	return map[string]any{"sessionUpdate": "usage_update", "cost": c.Cost}
}
`,
		"runtime/usage/store.go": `package usage

type Store struct{}

func (Store) Children() []Child {
	return []Child{{Cost: 1, Model: "m"}}
}
`,
		"backend/app/Ingestor.php": `<?php
namespace App;
class Ingestor {
    public function __construct(private Ledger $ledger) {}
    public function apply($e) {
        $this->always($e);
        match ($e->type) {
            'usage_update' => $this->usage($e),
            'turn_finished' => $this->turn($e),
            default => null,
        };
    }
    private function always($e) {}
    private function usage($e) { $this->ledger->record($e->cost); }
    private function turn($e) {}
}`,
		"backend/app/Ledger.php": `<?php
namespace App;
class Ledger {
    public function record(float $cost) {}
}`,
	}
}

func TestGoInterfacesFieldsAndVocabulary(t *testing.T) {
	root := t.TempDir()
	write(t, root, goFixture())
	g := index(t, root)

	report := "go:example.com/runtime/usage.Reporter.Report"
	provider := "go:example.com/runtime/usage.Provider.Children"
	store := "go:example.com/runtime/usage.Store.Children"
	if !hasEdge(g, report, provider, graph.Calls) {
		t.Errorf("a call through an interface goes to the interface method")
	}
	if !hasEdge(g, report, store, graph.Calls) {
		t.Errorf("and to every type that has the interface's methods")
	}
	if !hasEdge(g, store, "go:example.com/runtime/usage.Child.Cost", graph.Writes) {
		t.Errorf("a keyed literal writes the field")
	}
	if !hasEdge(g, report, "go:example.com/runtime/usage.Child.Cost", graph.Reads) {
		t.Errorf("c.Cost reads the field through a typed local")
	}
	if !hasEdge(g, report, "event:usage_update", graph.Produces) {
		t.Errorf("Go code that names an event type produces it (glob role)")
	}
	if !hasEdge(g, `php:App\Ingestor::apply`, "event:usage_update", graph.Consumes) {
		t.Errorf("the dispatcher consumes the event")
	}

	// from the Go store to the PHP ledger, across the event, without entering the other arm
	p := mustTrace(t, g, store, `php:App\Ledger::record`)
	var ids []string
	for _, h := range p {
		ids = append(ids, h.ID)
	}
	for _, id := range ids {
		if strings.Contains(id, "::turn") {
			t.Errorf("the turn_finished arm must not be on the path: %v", ids)
		}
	}
	hits, _ := g.Walk("event:usage_update", graph.Options{Mode: graph.ModeFlow, Dir: "down", MaxCost: 6, MinConf: 0.7})
	if hits[`php:App\Ingestor::usage`] == nil || hits[`php:App\Ingestor::always`] == nil {
		t.Errorf("the arm of the event and the unconditional call are reached")
	}
	if hits[`php:App\Ingestor::turn`] != nil {
		t.Errorf("the arm of another event must not be reached")
	}
	// configuration: golden flow and declared link
	cfg, _ := config.Load(root, "")
	if len(cfg.Golden) != 1 || len(cfg.Links) != 1 {
		t.Errorf("config not read: %+v", cfg)
	}
}

func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestReviewListsWhatTheChangeLeavesOut(t *testing.T) {
	root := t.TempDir()
	write(t, root, laravelFixture())
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")

	// change the handler of the create feature, nothing else
	p := filepath.Join(root, "app/Features/CreateEntry.php")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), "'hours' => $this->hours", "'hours' => $this->hours, 'note' => 'n'", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	g := index(t, root)
	changes, err := review.Changes(root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].File != "app/Features/CreateEntry.php" || len(changes[0].Ranges) == 0 {
		t.Fatalf("changes: %+v", changes)
	}
	rep := review.Run(g, "HEAD", changes, review.DefaultOptions())
	got := map[string]string{}
	for _, it := range rep.Items {
		got[it.Path] = it.Group
	}
	if got["tests/CreateEntryTest.php"] != "tests" {
		t.Errorf("the test that exercises the feature is related and untouched: %v", got)
	}
	if got["docs/entries.md"] != "docs" {
		t.Errorf("the document that names the class is related and untouched: %v", got)
	}
	if got["app/Mcp/Tools/CreateEntryTool.php"] == "" || got["app/Http/Controllers/EntryController.php"] == "" {
		t.Errorf("callers of the changed handler are related and untouched: %v", got)
	}
	if _, ok := got["app/Features/CreateEntry.php"]; ok {
		t.Errorf("the changed file itself is not 'left out'")
	}
	txt := review.Text(rep, 10)
	if !strings.Contains(txt, "tests/CreateEntryTest.php") {
		t.Errorf("text report: %s", txt)
	}
}

func TestFrontendAndPythonJoinTheBackend(t *testing.T) {
	root := t.TempDir()
	write(t, root, laravelFixture())
	g := index(t, root)

	book := "ts:resources/js/store/booking.ts#useBooking.book"
	create := "ts:resources/js/api/entries.ts#default.create"
	// a Vue handler reaches a composable's inner function through a destructured return value
	if !hasEdge(g, "ts:resources/js/components/Booking.vue#save", book, graph.Calls) {
		t.Errorf("`const { book } = useBooking(); book()` must resolve to the inner function of the composable")
	}
	if !hasEdge(g, book, create, graph.Calls) {
		t.Errorf("a call on a default-imported object literal (through the @/ alias) resolves to its method")
	}
	// the template calls the handler and renders the child component
	if !hasEdge(g, "ts:resources/js/components/Booking.vue", "ts:resources/js/components/Booking.vue#save", graph.Calls) {
		t.Errorf("@click=\"save\" in the template calls the script function")
	}
	if !hasEdge(g, "ts:resources/js/components/Booking.vue", "ts:resources/js/components/ChildCard.vue", graph.Uses) {
		t.Errorf("<ChildCard /> renders the imported component")
	}
	// HTTP: a URL without the backend's prefix, a template literal, and a generated client reached through useApi()
	if !hasEdge(g, create, "route:POST /api/v1/entries", graph.Requests) {
		t.Errorf("axios.post('v1/entries') must request POST /api/v1/entries")
	}
	if !hasEdge(g, "ts:resources/js/api/entries.ts#default.list", "route:GET /api/v1/entries", graph.Requests) {
		t.Errorf("a template literal URL with a query string must match the route")
	}
	if !hasEdge(g, "ts:resources/js/api/generated.ts#getApi.entriesIndex", "route:GET /api/v1/entries", graph.Requests) {
		t.Errorf("a generated client function requests its route")
	}
	if !hasEdge(g, "ts:resources/js/store/booking.ts#useBooking.load", "ts:resources/js/api/generated.ts#getApi.entriesIndex", graph.Calls) {
		t.Errorf("useApi().entriesIndex() resolves through the functions that return the client")
	}
	// string literals join the declared tool names at function level
	if !hasEdge(g, "ts:resources/js/store/booking.ts#useBooking.report", "mcp:monthly-report-tool", graph.Consumes) {
		t.Errorf("a TypeScript function that names a tool consumes it")
	}
	// Python: imports (relative, from-import of a module and of a name), calls, HTTP
	run := "py:client/app.py#run"
	if !hasEdge(g, run, "py:client/api.py#fetch", graph.Calls) || !hasEdge(g, run, "py:client/tools.py#call_tool", graph.Calls) {
		t.Errorf("`from . import api; api.fetch()` and `from .tools import call_tool` must resolve")
	}
	if !hasEdge(g, "py:client/api.py#fetch", "route:GET /api/v1/entries", graph.Requests) {
		t.Errorf("requests.get with an absolute URL must request the route")
	}
	if !hasEdge(g, run, "mcp:monthly-report-tool", graph.Consumes) {
		t.Errorf("a Python function that names a tool consumes it")
	}

	// the whole way: a click handler in a Vue component to the column the backend writes
	p := mustTrace(t, g, "ts:resources/js/components/Booking.vue#save", "column:entries.day")
	var ids []string
	for _, h := range p {
		ids = append(ids, h.ID)
	}
	want := []string{
		"ts:resources/js/components/Booking.vue#save", book, create, "route:POST /api/v1/entries",
		`php:App\Http\Controllers\EntryController::store`, `php:App\Features\CreateEntry::handle`, "column:entries.day",
	}
	if strings.Join(ids, "|") != strings.Join(want, "|") {
		t.Errorf("path\n got %v\nwant %v", ids, want)
	}
	// and a Python script that fetches the entries is reached from the column the same way
	p2 := mustTrace(t, g, "column:entries.note", run)
	if p2[len(p2)-1].ID != run {
		t.Errorf("a column read by the index action reaches the Python client that fetches it: %v", p2)
	}
}

func TestClusteringWritesCommunities(t *testing.T) {
	root := t.TempDir()
	write(t, root, laravelFixture())
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	g, st, err := build.Build(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Communities == 0 || st.Communities != len(g.Communities) {
		t.Fatalf("stats report %d communities, the graph has %d", st.Communities, len(g.Communities))
	}
	sizes := map[string]int{}
	for _, n := range g.Nodes {
		c, ok := n.Props["community"]
		switch {
		case n.Type == graph.TypeFile || n.Type == graph.TypeDoc || n.Type == graph.TypeTest:
			if ok {
				t.Errorf("%s (%s) is a file, it must not be in a community", n.ID, n.Type)
			}
		case ok:
			sizes[c]++
		}
	}
	for _, c := range g.Communities {
		if c.Size != sizes[strconv.Itoa(c.ID)] {
			t.Errorf("community %d has size %d, %d nodes carry its id", c.ID, c.Size, sizes[strconv.Itoa(c.ID)])
		}
	}
}

func TestClusteringCanBeDisabled(t *testing.T) {
	root := t.TempDir()
	write(t, root, laravelFixture())
	write(t, root, map[string]string{"impact.toml": "[cluster]\ndisabled = true\n"})
	g := index(t, root)
	if len(g.Communities) != 0 {
		t.Errorf("disabled clustering left %d communities", len(g.Communities))
	}
	for _, n := range g.Nodes {
		if _, ok := n.Props["community"]; ok {
			t.Fatalf("%s has a community although clustering is disabled", n.ID)
		}
	}
}

func TestGeneratedFilesStayInGraphButNotInGodNodes(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"src/generated/api.ts": "export function generatedCall(): number {\n  return 1;\n}\n",
		"src/app/use.ts":       "import { generatedCall } from '../generated/api';\n\nexport function run(): number {\n  return generatedCall();\n}\n",
	})
	g := index(t, root)

	gen := "ts:src/generated/api.ts#generatedCall"
	n := g.Nodes[gen]
	if n == nil {
		t.Fatalf("%s is not in the graph", gen)
	}
	if got := n.Props["file_kind"]; got != "generated" {
		t.Errorf("%s file_kind = %q, want generated", gen, got)
	}
	if file := g.Nodes["file:src/generated/api.ts"]; file == nil || file.Props["file_kind"] != "generated" {
		t.Errorf("the generated file node must carry file_kind generated: %+v", file)
	}
	if !hasEdge(g, "ts:src/app/use.ts#run", gen, graph.Calls) {
		t.Errorf("a call into generated code stays an edge")
	}
	for _, x := range g.GodNodes(0, graph.GodOptions{}) {
		if x.ID == gen {
			t.Errorf("generated %s must not be a god node", gen)
		}
	}
}

func TestMaxFilesRefusesALargeIndex(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a.go": "package a\n", "b.go": "package a\n", "c.go": "package a\n"})
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Index.MaxFiles = 2
	if _, _, err := build.Build(root, cfg); err == nil || !strings.Contains(err.Error(), "too many files to index: 3 (limit 2") {
		t.Fatalf("want a too many files error, got %v", err)
	}
	cfg.Index.MaxFiles = 3
	if _, _, err := build.Build(root, cfg); err != nil {
		t.Fatalf("at the limit the index must run: %v", err)
	}
}

const mountTypes = `package v1

type Resources struct {
	Limits map[string]string ` + "`json:\"limits\"`" + `
}

type Toleration struct {
	Key string ` + "`json:\"key\"`" + `
}

type AdapterSpec struct {
	Resources Resources ` + "`json:\"resources\"`" + `
	Port      int       ` + "`json:\"port,omitempty\"`" + `
	Hidden    string    ` + "`json:\"-\"`" + `
}

type EnvSpec struct {
	Adapter     AdapterSpec       ` + "`json:\"adapter\"`" + `
	Tolerations []Toleration      ` + "`json:\"tolerations\"`" + `
	Labels      map[string]string ` + "`json:\"labels\"`" + `
	Next        *EnvSpec          ` + "`json:\"next\"`" + `
}
`

const mountValues = `agentEnvironments:
  default:
    displayName: Default
    spec:
      adapter:
        resources:
          limits: {memory: 1536Mi}
        port: 8080
      tolerations:
        - key: workload
        - key: other
      labels: {team: a}
`

func TestMountJoinsAValuesFileToTheGoStructItFeeds(t *testing.T) {
	svc, deploy := t.TempDir(), t.TempDir()
	write(t, svc, map[string]string{
		"go.mod":                 "module example.com/svc\n\ngo 1.22\n",
		"api/types.go":           mountTypes,
		"values.yaml":            "agentEnvironments: {}\n",
		"impact.toml":            "[[mount]]\npath = \"agentEnvironments.*.spec\"\ngo_type = \"EnvSpec\"\nreason = \"the chart renders it into the spec of an Environment\"\n",
		"examples/x.values.yaml": mountValues,
	})
	write(t, deploy, map[string]string{"app/stage/values.yaml": mountValues + "other: 1\n"})

	gs := index(t, svc)
	const pat = "config:agentEnvironments.*.spec.adapter.resources"
	n := gs.Nodes[pat]
	if n == nil || n.Props[graph.PropConfigKind] != graph.ConfigPattern {
		t.Fatalf("pattern node missing: %v", n)
	}
	field := "go:example.com/svc/api.AdapterSpec.Resources"
	if !hasEdge(gs, pat, field, graph.Configures) {
		t.Fatalf("the pattern must point at the Go field")
	}
	for _, want := range []string{
		"config:agentEnvironments.*.spec",
		"config:agentEnvironments.*.spec.adapter.port",
		"config:agentEnvironments.*.spec.tolerations",
		"config:agentEnvironments.*.spec.tolerations.[].key",
		"config:agentEnvironments.*.spec.labels",
		"config:agentEnvironments.*.spec.next", // a recursive type ends at the field
	} {
		if !gs.Has(want) {
			t.Errorf("missing pattern %s", want)
		}
	}
	for _, not := range []string{"config:agentEnvironments.*.spec.adapter.hidden", "config:agentEnvironments.*.spec.adapter.Hidden", "config:agentEnvironments.*.spec.next.adapter"} {
		if gs.Has(not) {
			t.Errorf("must not exist: %s", not)
		}
	}
	// a values file of the same repository is joined while indexing
	ex := "config:examples/x.values.yaml#agentEnvironments.default.spec.adapter.resources"
	if !hasEdge(gs, ex, pat, graph.Configures) {
		t.Errorf("the example values file must meet the pattern")
	}
	if gs.Has("config:values.yaml#agentEnvironments.default") {
		t.Errorf("the empty map of values.yaml has no default key")
	}

	// the deployment repository has no configuration: it joins only after the merge
	gd := index(t, deploy)
	if len(gd.Nodes["config:app/stage/values.yaml#agentEnvironments.default.spec.adapter.resources"].Props) == 0 {
		t.Fatal("the key of the values file is a node")
	}
	for _, e := range gd.Edges {
		if e.Kind == graph.Configures {
			t.Fatalf("nothing to join to before the merge: %+v", e)
		}
	}
	m := graph.Merge(map[string]*graph.Graph{"acme/svc": gs, "acme/deploy": gd})
	key := "acme/deploy//config:app/stage/values.yaml#agentEnvironments.default.spec.adapter.resources"
	if !hasEdge(m, key, "acme/svc//"+pat, graph.Configures) {
		t.Fatalf("the merge must join the deployment key to the pattern")
	}
	if !hasEdge(m, "acme/deploy//config:app/stage/values.yaml#agentEnvironments.default.spec.tolerations.[].key", "acme/svc//config:agentEnvironments.*.spec.tolerations.[].key", graph.Configures) {
		t.Errorf("list elements must join too")
	}
	// the answer to "what does this value feed": the Go field
	o := graph.DefaultOptions()
	o.MaxCost = 10
	hits, _ := m.Walk("acme/deploy//config:app/stage/values.yaml#agentEnvironments.default.spec.adapter.resources", o)
	_, found := hits["acme/svc//"+field]
	if !found {
		t.Errorf("impact of the deployment value must reach the Go field")
	}
}
