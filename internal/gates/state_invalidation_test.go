package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPerCandidateStateIsResetBeforeItCanLeak checks the candidate loop's
// per-iteration state hygiene.
//
// The attempt package owns the state that describes one candidate's attempt —
// which provider was hit, what URL, what verdict that attempt ended on — and
// its BeginCandidate replaces the WHOLE state, so a per-candidate field added
// later is reset by construction rather than by someone extending a list of
// clears. What the type cannot hold is position: the two entry calls have to
// sit directly in the loop body, on the right side of its early exits, and
// that is control flow only this check can see. Two ways the positions rot:
//
//   - A call nested into a conditional stops being a per-iteration
//     guarantee: some iteration skips it, and a stale value survives.
//   - A call that drifts to the wrong SIDE of an early exit stops covering
//     it. ClearVerdict must precede the budget gate's `break`, or an
//     iteration that exits there reports the previous candidate's verdict as
//     this request's ending. BeginCandidate must precede the first
//     `continue`, or a dropped candidate carries the previous iteration's
//     provider and URL into the audit row — and it must stay AFTER the
//     break, because an exhausted chain is supposed to keep the last
//     attempt's identity, not wipe it.
func TestPerCandidateStateIsResetBeforeItCanLeak(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "gateway", "relay.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing relay.go: %v", err)
	}

	body := findCandidateLoopBody(t, file)
	callAt := topLevelAttemptCalls(body)

	firstBreak := firstIndexWithExit(body, token.BREAK)
	firstContinue := firstIndexWithExit(body, token.CONTINUE)

	clearIdx, ok := callAt["ClearVerdict"]
	if !ok {
		t.Error("the candidate loop has no top-level rc.attempt.ClearVerdict() call: either it " +
			"was removed, or it moved inside a conditional — both let a verdict from the " +
			"previous candidate survive into an iteration that never makes an attempt")
	} else if firstBreak >= 0 && clearIdx > firstBreak {
		t.Errorf("rc.attempt.ClearVerdict() sits at loop statement %d, after the first early "+
			"exit at statement %d: an iteration that exits there reports the PREVIOUS "+
			"candidate's verdict as this request's ending", clearIdx, firstBreak)
	}

	beginIdx, ok := callAt["BeginCandidate"]
	if !ok {
		t.Error("the candidate loop has no top-level rc.attempt.BeginCandidate() call: either " +
			"it was removed, or it moved inside a conditional — both leave a stale provider " +
			"and URL from the previous candidate readable while this one is judged")
	} else {
		if firstContinue >= 0 && beginIdx > firstContinue {
			t.Errorf("rc.attempt.BeginCandidate() sits at loop statement %d, after the first "+
				"continue at statement %d: iterations taking that path carry the previous "+
				"candidate's identity", beginIdx, firstContinue)
		}
		if firstBreak >= 0 && beginIdx < firstBreak {
			t.Errorf("rc.attempt.BeginCandidate() sits at loop statement %d, before the budget "+
				"gate's break at statement %d: an iteration that exits there now wipes the "+
				"last attempt's identity, and the audit row files an exhausted chain under "+
				"no provider at all", beginIdx, firstBreak)
		}
	}
}

// findCandidateLoopBody locates the body of the loop that walks candidates
// inside relayCandidates. The loop ranges over a slice, which the Go AST
// represents as a RangeStmt; a classic ForStmt is accepted too so an index
// rewrite does not make this check silently stop finding anything.
func findCandidateLoopBody(t *testing.T, file *ast.File) *ast.BlockStmt {
	t.Helper()
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == "relayCandidates" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("relayCandidates not found in relay.go; the candidate loop moved and this " +
			"check needs to be pointed at wherever it lives now")
	}
	for _, stmt := range fn.Body.List {
		switch s := stmt.(type) {
		case *ast.RangeStmt:
			return s.Body
		case *ast.ForStmt:
			return s.Body
		}
	}
	t.Fatal("relayCandidates has no top-level range/for loop; the candidate iteration was " +
		"restructured and this check needs rewriting against its new shape")
	return nil
}

// topLevelAttemptCalls returns, for each method called on rc.attempt by a
// statement sitting directly in the loop body, the index of that statement in
// the body's list.
func topLevelAttemptCalls(body *ast.BlockStmt) map[string]int {
	out := map[string]int{}
	for i, stmt := range body.List {
		es, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "attempt" {
			continue
		}
		if recv, ok := inner.X.(*ast.Ident); !ok || recv.Name != "rc" {
			continue
		}
		if _, seen := out[sel.Sel.Name]; !seen {
			out[sel.Sel.Name] = i
		}
	}
	return out
}

// firstIndexWithExit returns the index of the first top-level statement that
// contains a bare break or continue binding to the candidate loop itself, or
// -1 if none does.
//
// Binding rules make the walk asymmetric: a nested for/range captures both
// kinds, so neither walk descends into one; a switch or select captures only
// break, so the continue walk descends into them and the break walk does not.
// Labeled branches are skipped entirely — a labeled break targets whatever the
// label names, which this check has no business guessing.
func firstIndexWithExit(body *ast.BlockStmt, kind token.Token) int {
	for i, stmt := range body.List {
		found := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			if found {
				return false
			}
			switch inner := n.(type) {
			case *ast.ForStmt, *ast.RangeStmt:
				return false // rebinds both break and continue
			case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				return kind != token.BREAK // rebinds break only
			case *ast.BranchStmt:
				if inner.Tok == kind && inner.Label == nil {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return i
		}
	}
	return -1
}

// TestReleaseIsArmedBeforeTheSettlementSafetyNet pins a defer registration
// order in the request handler that nothing else can reach.
//
// Deferred functions run last-registered-first. The admission release must be
// REGISTERED before the panic-settlement defer so that on an unwind it runs
// AFTER settlement: a release is a reconciliation against how the request
// ended, and run first it reads a zero status and a blank reason on exactly
// the requests that ended worst. The two registrations sit a few lines apart
// in one function, which is what makes the order easy to flip in a refactor
// and invisible to every behavioural test that does not panic mid-request.
func TestReleaseIsArmedBeforeTheSettlementSafetyNet(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "gateway", "relay.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing relay.go: %v", err)
	}

	var handle *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "Handle" {
			handle = fd
			break
		}
	}
	if handle == nil {
		t.Fatal("Handle not found in relay.go; the handler moved and this check needs re-aiming")
	}

	releaseLine, settleLine := 0, 0
	ast.Inspect(handle, func(n ast.Node) bool {
		def, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		text := renderCallSource(fset, def)
		if releaseLine == 0 && strings.Contains(text, "releaseAdmissions") {
			releaseLine = fset.Position(def.Pos()).Line
		}
		if settleLine == 0 && strings.Contains(text, "logWritten") {
			settleLine = fset.Position(def.Pos()).Line
		}
		return true
	})
	if releaseLine == 0 || settleLine == 0 {
		t.Fatalf("could not find both defers (release at %d, settlement at %d); Handle was "+
			"restructured and this check needs rewriting", releaseLine, settleLine)
	}
	if releaseLine > settleLine {
		t.Errorf("the release defer is registered at line %d, after the settlement safety net "+
			"at line %d: on a panic the release now runs before settlement and reconciles "+
			"against a zero status", releaseLine, settleLine)
	}

	// There must be exactly ONE construction of the exchange's Outcome in the
	// whole package — the one finalize stores — and it must state every field
	// the type has. Both the recorders and the admission releases read that
	// single record; a second literal at any other site would let two accounts
	// of the same ending drift, and a construction that fills two fields and
	// lets four zero silently hands every capability a request that looks like
	// it never ran. Asserted against the type's own field count so adding a
	// field to Outcome turns this red until the construction says what its
	// value is. Test files are exempt: a test building an Outcome is stating
	// an expectation, not keeping the books.
	constructions := outcomeConstructions(t)
	if len(constructions) != 1 {
		t.Errorf("found %d fact.Outcome constructions in non-test gateway code, want exactly "+
			"one (finalize's): every extra site is a second account of the same ending "+
			"that can drift from the first — %v", len(constructions), constructionSites(constructions))
	}
	for _, c := range constructions {
		if !strings.HasPrefix(c.pos, "internal/gateway/log.go:") {
			t.Errorf("the Outcome construction sits at %s, not in log.go's finalize: the release "+
				"and the recorders are both documented to read what FINALIZE settled, so the "+
				"construction moving elsewhere means that documentation now lies", c.pos)
		}
	}
	outcomeFields := map[string]bool{}
	for _, c := range constructions {
		for _, elt := range c.lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					outcomeFields[id.Name] = true
				}
			}
		}
	}
	for _, field := range outcomeFieldNames(t) {
		if !outcomeFields[field] {
			t.Errorf("the Outcome construction never sets %s: a zeroed field is indistinguishable "+
				"from a request that never ran, and the capability reading it cannot tell", field)
		}
	}
}

// outcomeConstruction is one fact.Outcome composite literal and where it sits.
type outcomeConstruction struct {
	pos string
	lit *ast.CompositeLit
}

// outcomeConstructions finds every fact.Outcome composite literal in the
// gateway package's non-test files, through the same shared walk every other
// check uses. Test files are exempt for the reason the caller states: a test
// building an Outcome is stating an expectation, not keeping the books.
func outcomeConstructions(t *testing.T) []outcomeConstruction {
	t.Helper()
	var out []outcomeConstruction
	for _, f := range parseTree(t, filepath.Join("internal", "gateway")) {
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := cl.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Outcome" {
				rel, line := f.pos(cl)
				out = append(out, outcomeConstruction{pos: fmt.Sprintf("%s:%d", rel, line), lit: cl})
			}
			return true
		})
	}
	return out
}

func constructionSites(cs []outcomeConstruction) []string {
	sites := make([]string, len(cs))
	for i, c := range cs {
		sites[i] = c.pos
	}
	return sites
}

// outcomeFieldNames reads fact.Outcome's declared fields from source, so the
// completeness assertion above tracks the type instead of a hand-kept list.
func outcomeFieldNames(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "internal", "fact", "snapshot.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing snapshot.go: %v", err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Outcome" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				out = append(out, name.Name)
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatal("fact.Outcome not found in snapshot.go; the type moved and this check needs re-aiming")
	}
	return out
}

// renderCallSource prints a defer statement back to source text, for matching
// which defer is which without depending on statement internals.
func renderCallSource(fset *token.FileSet, n ast.Node) string {
	var sb strings.Builder
	_ = printer.Fprint(&sb, fset, n)
	return sb.String()
}
