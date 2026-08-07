package gates

import (
	"go/ast"
	"strings"
	"testing"
)

// TestARecognisedRecordCaseReadsEveryFieldItDeclares guards the overflow
// contract from the inside.
//
// fact.Record is deliberately open — a record type the consumer has never
// heard of lands in the overflow column intact rather than being dropped, so
// "every type has its own case" is not a property this design wants. The
// failure it does want to prevent is the opposite one, and it has actually
// shipped here before: a case arm that DOES exist for a type copies the fields
// it happened to need and silently drops the rest — worse than being
// unrecognised, because the unrecognised path keeps everything and the
// recognised one has no default branch left to catch what it missed.
//
// This walks every type-switch case whose type is a known Record and asserts
// the case body accounts for the switch variable's every field. Two shapes are
// exempt, each for a stated reason:
//
//   - A case listing several types binds its variable as the interface type,
//     which cannot do a field access at all — there is nothing this check
//     could demand of it.
//   - A case that passes the whole value to a function is delegating, not
//     picking apart; whether the callee reads every field is the callee's
//     contract. Only a genuine call argument counts — a bare mention such as
//     a blank assignment silences nothing here.
func TestARecognisedRecordCaseReadsEveryFieldItDeclares(t *testing.T) {
	files := parseTree(t, "internal")
	registry := discoverRecordTypes(t, files)

	// A rewrite that replaced every type switch with something this walk does
	// not recognise would leave the check inspecting nothing, and a pass over
	// nothing is indistinguishable from a pass. Count what was actually
	// examined and refuse to conclude anything from zero.
	casesExamined := 0

	for _, f := range files {
		aliases := importAliases(f.ast)
		ast.Inspect(f.ast, func(n ast.Node) bool {
			sw, ok := n.(*ast.TypeSwitchStmt)
			if !ok {
				return true
			}
			bindVar := typeSwitchBinding(sw)
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				if len(cc.List) != 1 {
					// Multiple types in one case bind the variable as the
					// interface type; a field access is a compile error there,
					// so there is nothing to check.
					continue
				}
				qname, resolved := resolveTypeExpr(cc.List[0], aliases)
				if !resolved {
					continue
				}
				rt, known := registry[qname]
				if !known {
					continue
				}
				if bindVar == "" {
					// `switch x.(type)` with no binding cannot read any field —
					// not a violation, but nothing checkable either.
					continue
				}
				casesExamined++
				if wholeValuePassedToACall(cc.Body, bindVar) {
					continue
				}
				touched := fieldsReadFrom(cc.Body, bindVar)
				for _, fieldName := range rt.fieldNames() {
					if !touched[fieldName] {
						rel, line := f.pos(cc)
						t.Errorf("%s:%d: case %s reads %s but never touches its %s field — "+
							"a case that copies part of a record and drops the rest is worse "+
							"than the type going unrecognised, because there is no overflow "+
							"branch left to catch what it dropped",
							rel, line, qname, bindVar, fieldName)
					}
				}
			}
			return true
		})
	}

	if casesExamined == 0 {
		t.Fatal("no type-switch case over a known Record type was found anywhere; " +
			"either the consumers were restructured out of this check's sight or the " +
			"discovery broke — both mean this check verified nothing")
	}
}

// importAliases maps a file's local name for each import to its real import
// path, so a case type can be resolved even when the file aliases the package.
func importAliases(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		local := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			local = spec.Name.Name
		}
		out[local] = path
	}
	return out
}

// resolveTypeExpr turns a case-clause type expression into the qualified name
// the discovery uses as a key ("import/path.TypeName"), or reports false when
// the expression is not a simple package-qualified type (a case on nil, or a
// locally declared type with no qualifier).
func resolveTypeExpr(e ast.Expr, aliases map[string]string) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	path, ok := aliases[pkgIdent.Name]
	if !ok {
		return "", false
	}
	return path + "." + sel.Sel.Name, true
}

// typeSwitchBinding returns the variable name a type switch binds its subject
// to — "rec" in `switch rec := e.Record.(type)` — or "" if the switch has no
// binding.
func typeSwitchBinding(sw *ast.TypeSwitchStmt) string {
	as, ok := sw.Assign.(*ast.AssignStmt)
	if !ok || len(as.Lhs) != 1 {
		return ""
	}
	id, ok := as.Lhs[0].(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}

// wholeValuePassedToACall reports whether the switch-bound identifier appears
// as an argument in a function or method call anywhere in the case body.
//
// That is the one escape this check allows, and it is deliberately this
// narrow. Delegating the record to a helper is the legitimate way to handle
// fields this arm has no column for — the helper owns the rest of the record,
// and this check cannot see through a call. But nothing short of a call
// qualifies: assigning the value to blank, aliasing it to another name, or
// otherwise mentioning it without handing it anywhere reads no fields and
// keeps none, and treating those as delegation would let one throwaway line
// switch the whole check off.
func wholeValuePassedToACall(body []ast.Stmt, bindVar string) bool {
	found := false
	for _, stmt := range body {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, arg := range call.Args {
				if id, ok := arg.(*ast.Ident); ok && id.Name == bindVar {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// fieldsReadFrom collects every field name accessed off one identifier —
// bindVar dot something — anywhere in a block of statements.
func fieldsReadFrom(body []ast.Stmt, bindVar string) map[string]bool {
	touched := map[string]bool{}
	for _, stmt := range body {
		ast.Inspect(stmt, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == bindVar {
				touched[sel.Sel.Name] = true
			}
			return true
		})
	}
	return touched
}
