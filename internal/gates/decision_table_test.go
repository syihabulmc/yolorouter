package gates

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decision-table completeness and combine-fold properties are behavioural
// tests living next to the code they exercise, in internal/gateway — they call
// real production functions, which a source-text check cannot. This file holds
// the two structural properties no behavioural test can express, because there
// is no input that makes "a Record type has a field shaped like a routing
// effect" or "code outside decision.go reads a routing-relevant field of
// fact.Fact" observable at runtime. Both are silent until the day someone
// reads the wrong field under load — which from the outside looks like no bug
// at all: no error, no panic, just a decision the table never made.

// TestNoRecordCarriesALoopEffect keeps the accounting half of the fact
// vocabulary unable to smuggle control flow.
//
// fact.Record is the accounting half; fact.Fact is the routing half, and
// Fact.Kind is the only input the decision table takes. The split is enforced
// today because Record's method set has nowhere to put a LoopEffect — but
// nothing stops a future Record type from carrying a field named Loop or typed
// LoopEffect, and once persisted next to a Kind it happens to correlate with,
// that field is an easy thing for a later reader to reach for "just this
// once". This check keeps the field from existing at all.
//
// The type comparison strips a pointer and matches the bare name as well as
// any package-qualified spelling, because a Record may be declared inside the
// gateway package itself (test fakes already are) where LoopEffect needs no
// qualifier.
func TestNoRecordCarriesALoopEffect(t *testing.T) {
	files := parseTree(t, "internal")
	for _, rt := range discoverRecordTypes(t, files) {
		for _, f := range rt.fields {
			bare := strings.TrimPrefix(f.typeStr, "*")
			typeIsLoop := bare == "LoopEffect" || strings.HasSuffix(bare, ".LoopEffect")
			if f.name == "Loop" || typeIsLoop {
				t.Errorf("%s:%d: %s.%s looks like a routing effect on an accounting record; "+
					"Record types must not carry anything a future reader could mistake for "+
					"steering the relay loop", rt.file, rt.line, rt.name, f.name)
			}
		}
	}
}

// TestOnlyDecisionTableReadsRoutingFactFields keeps the decision table the
// single place a routing fact is turned into a decision.
//
// fact.Fact's own field comments draw the line: Kind is the one field the
// decision table keys on, Reporter is logging-only and legitimately stamped
// throughout the kernel, Detail and Status feed the status-passthrough fold,
// and Reason is the persisted refusal code. The fold and the code both belong
// to decision.go (resolveBatch and the helpers around it); business logic
// anywhere else that branches on Status, Detail, or Reason is a second,
// unaudited place a routing decision gets made — invisible to anything that
// audits the table for completeness.
//
// This is the one check that needs real type information rather than syntax:
// Status, Detail, and Reason are common field names, and only a type checker
// can tell a fact.Fact's Status from any other type's. Test files are excluded
// — a test reaching into a Fact to build a fixture is not a runtime bypass.
func TestOnlyDecisionTableReadsRoutingFactFields(t *testing.T) {
	root := repoRoot(t)
	mod := modulePath(t)
	gwDir := filepath.Join(root, "internal", "gateway")

	fset := token.NewFileSet()
	var files []*ast.File
	entries, err := os.ReadDir(gwDir)
	if err != nil {
		t.Fatalf("reading internal/gateway: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(gwDir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", e.Name(), perr)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no production files found under internal/gateway; the check would pass by finding nothing")
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	cfg := &types.Config{
		Importer: importer.ForCompiler(fset, "source", nil),
		Error:    func(err error) { t.Logf("type-check note: %v", err) },
	}
	if _, err := cfg.Check(mod+"/internal/gateway", fset, files, info); err != nil {
		t.Logf("type-checking internal/gateway surfaced errors (see go build for the "+
			"authoritative list): %v", err)
	}

	factFactType := mod + "/internal/fact.Fact"
	// Counts every selector that resolved to a fact.Fact field, gated or not.
	// If the importer breaks, no selector resolves, and without this the check
	// would report a clean pass over code it never actually saw.
	resolvedFactSelectors := 0

	for _, f := range files {
		pos := fset.Position(f.Pos())
		isDecisionFile := filepath.Base(pos.Filename) == "decision.go"
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			xt, ok := info.Types[sel.X]
			if !ok || xt.Type == nil {
				return true
			}
			named := namedTypeOf(xt.Type)
			if named == nil || named.String() != factFactType {
				return true
			}
			resolvedFactSelectors++
			if isDecisionFile {
				return true // the trusted fold
			}
			switch sel.Sel.Name {
			case "Status", "Detail", "Reason":
				p := fset.Position(sel.Pos())
				rel, _ := filepath.Rel(root, p.Filename)
				t.Errorf("%s:%d: reads fact.Fact.%s outside decision.go — routing decisions "+
					"must go through the decision table; branching on this field here is a "+
					"second, unaudited place the relay loop can be steered from",
					filepath.ToSlash(rel), p.Line, sel.Sel.Name)
			}
			return true
		})
	}

	if resolvedFactSelectors == 0 {
		t.Fatal("no fact.Fact field access resolved anywhere in internal/gateway; " +
			"the type importer has broken and this check inspected nothing")
	}
}

// namedTypeOf unwraps a pointer to reach the named type underneath, since
// fact.Fact is read both by value and by pointer.
func namedTypeOf(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := t.(*types.Named)
	return n
}
