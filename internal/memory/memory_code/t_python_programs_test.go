package memory_code

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antlr4-go/antlr/v4"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_code/pyparser"
)

// syntaxErrors collects the diagnostics of the lexer and the parser, which extractPython drops.
type syntaxErrors struct {
	*antlr.DefaultErrorListener
	found *[]string
}

// programCase is a Python program of testdata/python, the minimal number of lines it has, and edges that its graph must have.
type programCase struct {
	file     string
	minLines int
	edges    [][3]string
	entities map[string]string
}

func (s syntaxErrors) SyntaxError(_ antlr.Recognizer,
	_ interface{},
	line,
	col int,
	msg string,
	_ antlr.RecognitionException) {
	*s.found = append(*s.found, fmt.Sprintf("%d:%d %s", line, col, msg))
}

// parseDiagnostics is the syntax errors that the lexer and the parser report for the code.
func parseDiagnostics(code string) []string {
	found := []string{}
	lexer := pyparser.NewPython3Lexer(antlr.NewInputStream(code))
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(syntaxErrors{antlr.NewDefaultErrorListener(), &found})
	parser := pyparser.NewPython3Parser(antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel))
	parser.RemoveErrorListeners()
	parser.AddErrorListener(syntaxErrors{antlr.NewDefaultErrorListener(), &found})
	parser.File_input()
	return found
}

var programCases = []programCase{
	{
		file:     "p01_bank.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "decimal.Decimal"},
			{"module", "imports", "typing.Optional"},
			{"InsufficientFunds", "inherits", "Exception"},
			{"Savings", "inherits", "Account"},
			{"Checking", "defines", "Checking.withdraw"},
			{"Account.deposit", "calls", "Account.validate"},
			{"Account.deposit", "calls", "Account.record"},
			{"Account.withdraw", "calls", "InsufficientFunds"},
			{"Savings.add_interest", "calls", "Account.deposit"},
			{"transfer", "calls", "log.warning"},
			{"main", "calls", "transfer"},
		},
		entities: map[string]string{
			"Account.open":      TypeMethod,
			"InsufficientFunds": TypeClass,
			"transfer":          TypeFunction,
		},
	},
	{
		file:     "p02_counter_closures.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "functools"},
			{"make_counter", "defines", "make_counter.tick"},
			{"compose", "defines", "compose.pipe"},
			{"curry", "defines", "curry.collect"},
			{"curry.collect", "defines", "curry.collect.inner"},
			{"curry.collect.inner", "calls", "curry.collect"},
			{"timed", "defines", "timed.decorator"},
			{"timed.decorator", "defines", "timed.decorator.wrapper"},
			{"timed.decorator.wrapper", "calls", "time.perf_counter"},
			{"counter_demo", "calls", "make_counter"},
			{"pipeline_demo", "calls", "compose"},
		},
		entities: map[string]string{
			"curry.collect.inner":     TypeFunction,
			"timed.decorator.wrapper": TypeFunction,
		},
	},
	{
		file:     "p03_shapes.py",
		minLines: 90,
		edges: [][3]string{
			{"module", "imports", "abc.ABC"},
			{"Shape", "inherits", "ABC"},
			{"Circle", "inherits", "Shape"},
			{"Square", "inherits", "Rect"},
			{"Square", "defines", "Square.__init__"},
			{"Group", "defines", "Group.largest"},
			{"Triangle.area", "calls", "self.perimeter"},
			{"Triangle.area", "calls", "math.sqrt"},
			{"report", "calls", "s.describe"},
			{"main", "calls", "report"},
			{"main", "calls", "biggest"},
		},
		entities: map[string]string{
			"Shape.describe": TypeMethod,
			"Group":          TypeClass,
		},
	},
	{
		file:     "p04_generators.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "collections.deque"},
			{"take", "calls", "list"},
			{"primes", "calls", "naturals"},
			{"chunked", "calls", "take"},
			{"window", "calls", "deque"},
			{"Countdown", "defines", "Countdown.__iter__"},
			{"Countdown.__iter__", "calls", "CountdownIter"},
			{"CountdownIter", "defines", "CountdownIter.__next__"},
			{"demo", "calls", "primes"},
			{"demo", "calls", "flatten"},
		},
		entities: map[string]string{
			"CountdownIter.__next__": TypeMethod,
			"flatten":                TypeFunction,
		},
	},
	{
		file:     "p05_async.py",
		minLines: 90,
		edges: [][3]string{
			{"module", "imports", "asyncio"},
			{"RateLimiter", "defines", "RateLimiter.__aenter__"},
			{"RateLimiter.__aenter__", "calls", "asyncio.sleep"},
			{"fetch", "calls", "asyncio.sleep"},
			{"worker", "calls", "fetch"},
			{"worker", "calls", "RateLimiter"},
			{"Crawler.run", "calls", "worker"},
			{"Crawler.run_with_timeout", "calls", "Crawler.run"},
			{"main", "calls", "Crawler"},
			{"main", "calls", "collect"},
		},
		entities: map[string]string{
			"session":     TypeFunction,
			"Crawler.run": TypeMethod,
		},
	},
	{
		file:     "p06_tokenizer.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "re"},
			{"module", "imports", "collections.namedtuple"},
			{"ParseError", "inherits", "Exception"},
			{"Parser", "defines", "Parser.factor"},
			{"Parser.parse", "calls", "Parser.expr"},
			{"Parser.expr", "calls", "Parser.term"},
			{"Parser.term", "calls", "Parser.factor"},
			{"Parser.factor", "calls", "Parser.expect"},
			{"calc", "calls", "Parser"},
			{"calc", "calls", "tokenize"},
		},
		entities: map[string]string{
			"Parser.advance": TypeMethod,
			"evaluate":       TypeFunction,
		},
	},
	{
		file:     "p07_inventory.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "enum.Enum"},
			{"Category", "inherits", "Enum"},
			{"Registry", "inherits", "type"},
			{"Item", "inherits", "JsonMixin"},
			{"Item", "inherits", "ComparableMixin"},
			{"Perishable", "inherits", "Item"},
			{"Perishable.total", "calls", "super"},
			{"JsonMixin.to_json", "calls", "json.dumps"},
			{"Inventory.__add__", "calls", "Inventory"},
			{"Inventory.dump", "calls", "json.dumps"},
			{"build", "calls", "Inventory"},
			{"build", "calls", "Perishable"},
		},
		entities: map[string]string{
			"Inventory.__contains__": TypeMethod,
		},
	},
	{
		file:     "p08_state_machine.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "defines", "EventBus"},
			{"EventBus", "defines", "EventBus.on"},
			{"EventBus.on", "defines", "EventBus.on.register"},
			{"Machine", "defines", "Machine.run_script"},
			{"Machine.run_script", "defines", "Machine.run_script.step"},
			{"Machine.run_script.step", "calls", "Machine.go"},
			{"Machine.go", "calls", "Machine.can"},
			{"make_guarded", "defines", "make_guarded.guarded"},
			{"history", "defines", "history.fmt"},
			{"demo", "calls", "make_guarded"},
			{"demo", "calls", "history"},
		},
		entities: map[string]string{
			"make_guarded.guarded": TypeFunction,
			"log_start":            TypeFunction,
		},
	},
	{
		file:     "p09_graph.py",
		minLines: 100,
		edges: [][3]string{
			{"module", "imports", "heapq"},
			{"Graph", "defines", "Graph.dfs"},
			{"Graph.dfs", "defines", "Graph.dfs.visit"},
			{"Graph.dfs.visit", "calls", "Graph.neighbors"},
			{"Graph.bfs", "calls", "Graph.neighbors"},
			{"Graph.dijkstra", "calls", "heapq.heappush"},
			{"Graph.components", "calls", "Graph.bfs"},
			{"from_pairs", "calls", "Graph"},
			{"shortest", "calls", "g.dijkstra"},
		},
		entities: map[string]string{
			"Graph.dfs.visit": TypeFunction,
			"Graph.topo_sort": TypeMethod,
		},
	},
	{
		file:     "p10_cache_decorators.py",
		minLines: 85,
		edges: [][3]string{
			{"module", "imports", "contextlib.contextmanager"},
			{"LRU", "defines", "LRU.put"},
			{"LRU.put", "calls", "self.data.move_to_end"},
			{"cached", "defines", "cached.__call__"},
			{"cached.__call__", "defines", "cached.__call__.wrapper"},
			{"cached.__call__.wrapper", "calls", "time.time"},
			{"locked", "calls", "lock.acquire"},
			{"stopwatch", "calls", "time.time"},
			{"run", "calls", "stopwatch"},
			{"run", "calls", "slow_square"},
		},
		entities: map[string]string{
			"cached.__call__.wrapper": TypeFunction,
			"cached":                  TypeClass,
		},
	},
	{
		file:     "p11_library_system.py",
		minLines: 240,
		edges: [][3]string{
			{"module", "imports", "datetime"},
			{"module", "imports", "typing.Iterator"},
			{"NotAvailable", "inherits", "LibraryError"},
			{"LibraryError", "inherits", "Exception"},
			{"PremiumMember", "inherits", "Member"},
			{"audited", "defines", "audited.decorator"},
			{"audited.decorator", "defines", "audited.decorator.wrapper"},
			{"Catalog.take", "calls", "Catalog.get"},
			{"Catalog.take", "calls", "NotAvailable"},
			{"Loan.fine", "calls", "Loan.overdue_days"},
			{"Library.borrow", "calls", "Loan"},
			{"Library.report", "defines", "Library.report.line"},
			{"make_reminder", "defines", "make_reminder.remind"},
			{"make_searcher", "defines", "make_searcher.search"},
			{"main", "calls", "seed"},
			{"main", "calls", "make_reminder"},
			{"main", "calls", "make_searcher"},
		},
		entities: map[string]string{
			"Library.report.line":  TypeFunction,
			"make_reminder.remind": TypeFunction,
			"Catalog.give_back":    TypeMethod,
			"Library.give_back":    TypeMethod,
		},
	},
	{
		file:     "p12_interpreter.py",
		minLines: 220,
		edges: [][3]string{
			{"module", "imports", "operator"},
			{"module", "imports", "sys"},
			{"LispError", "inherits", "Exception"},
			{"Symbol", "inherits", "str"},
			{"Env", "inherits", "dict"},
			{"Procedure.__call__", "calls", "evaluate"},
			{"Procedure.__call__", "calls", "Env"},
			{"parse", "calls", "read_from"},
			{"parse", "calls", "tokenize"},
			{"standard_env", "calls", "Env"},
			{"make_repl", "defines", "make_repl.step"},
			{"make_repl", "defines", "make_repl.loop"},
			{"make_repl.step", "calls", "run"},
			{"make_repl.loop", "calls", "make_repl.step"},
			{"self_test", "calls", "fact_program"},
		},
		entities: map[string]string{
			"Env.find":       TypeMethod,
			"make_repl.step": TypeFunction,
		},
	},
	{
		file:     "p13_pipeline_framework.py",
		minLines: 220,
		edges: [][3]string{
			{"module", "imports", "abc"},
			{"module", "imports", "statistics"},
			{"register", "defines", "register.decorate"},
			{"select", "defines", "select.run"},
			{"where", "defines", "where.run"},
			{"sort_by", "defines", "sort_by.run"},
			{"group_agg", "defines", "group_agg.run"},
			{"CsvSource", "inherits", "Source"},
			{"ListSink", "inherits", "Sink"},
			{"CsvSink", "inherits", "Sink"},
			{"Pipeline.then", "calls", "lookup"},
			{"Pipeline.then", "calls", "Pipeline.pipe"},
			{"Pipeline._instrument", "defines", "Pipeline._instrument.counted"},
			{"Pipeline.run", "calls", "ListSink"},
			{"make_threshold", "defines", "make_threshold.check"},
			{"make_bucketer", "defines", "make_bucketer.bucket"},
			{"build_report", "calls", "Pipeline"},
			{"build_report", "calls", "CsvSource"},
		},
		entities: map[string]string{
			"Pipeline._instrument.counted": TypeFunction,
			"CsvSink.write":                TypeMethod,
		},
	},
}

func hasEdge(g Graph,
	e [3]string) bool {
	for _, have := range g.Edges {
		if have.Source == e[0] && have.Relation == e[1] && have.Target == e[2] {
			return true
		}
	}
	return false
}

// TestPythonPrograms parses programs of 85 to 250 lines (classes, nested functions, closures, decorators, generators, async, ...): the
// lexer and the parser report no syntax error, and the graph has the expected structure.
func TestPythonPrograms(t *testing.T) {
	for _, tc := range programCases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "python", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			code := string(raw)
			if n := strings.Count(code, "\n"); n < tc.minLines {
				t.Fatalf("program has %d lines, want at least %d", n, tc.minLines)
			}
			if diags := parseDiagnostics(code); len(diags) > 0 {
				t.Fatalf("syntax errors: %v", diags)
			}
			g, err := ExtractAuto(code)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range tc.edges {
				if !hasEdge(g, e) {
					t.Errorf("missing edge %v", e)
				}
			}
			types := map[string]string{}
			for _, e := range g.Entities {
				types[e.Name] = e.Type
			}
			for name, typ := range tc.entities {
				if types[name] != typ {
					t.Errorf("entity %s: type %q, want %q", name, types[name], typ)
				}
			}
		})
	}
}

// TestPythonProgramsAreComplete makes sure that no program of testdata/python is left out of programCases.
func TestPythonProgramsAreComplete(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "python", "*.py"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(programCases) {
		t.Fatalf("%d programs in testdata, %d cases", len(files), len(programCases))
	}
}
