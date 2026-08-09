package gates

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestErrcodeRegistryIsCoherent holds pkg/errcode's three parallel
// declarations — the integer code constants, the ErrorMessages map, and the
// Err* sentinels — to one another, for every code at once.
//
// The invariant used to be asserted by a fresh copy-paste test each time a
// family of codes was added, which meant a family whose author forgot the
// test copy was simply unchecked. The hazard is not hypothetical: a sentinel
// is built as errors.New(ErrorMessages[X]), so a constant missing its map
// entry does not fail anywhere — it quietly becomes a sentinel whose text is
// the empty string.
//
// Checked, for every declaration in the package:
//   - every code constant's literal value is unique;
//   - every code constant has a non-empty ErrorMessages entry;
//   - every ErrorMessages key is a declared code constant;
//   - every sentinel is named Err<Code> and built as
//     errors.New(ErrorMessages[<Code>]) for that same code — never from a
//     string literal, never from another code's message, never through any
//     other constructor;
//   - the frontend's numeric mirrors agree with the backend declarations
//     (see assertFrontendMirrorsAgree).
//
// Two boundaries are deliberate, not oversights:
//   - The gate does not require any particular sentinel to EXIST. A sentinel
//     with consumers cannot be deleted (the build breaks); one without
//     consumers is a deletion candidate under this repo's own dead-surface
//     discipline, and a hand-kept "required sentinels" list would revive the
//     copy-a-list-per-family maintenance this gate exists to end. Deleting an
//     exported sentinel is a loud diff line; the gate's job is the
//     incoherence a diff does not show.
//   - The locale tables key messages by bare number, so swapping the values
//     of two backend codes that appear ONLY there (not among the checked UI
//     branching constants) would show the wrong message text while every
//     number still exists. Closing that needs the locale tables keyed by
//     name or generated from this registry — a frontend restructuring
//     deferred until the risk is real; the constants the UI branches on are
//     already checked by name AND value.
func TestErrcodeRegistryIsCoherent(t *testing.T) {
	codes := map[string]string{}     // const name -> literal value
	messageKeys := map[string]bool{} // idents keying ErrorMessages
	emptyMessages := []string{}      // keys whose message is ""
	type sentinel struct{ name, arg string }
	var sentinels []sentinel
	var badSentinels []string

	for _, f := range parseTree(t, "pkg/errcode") {
		if strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		for _, decl := range f.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.CONST:
				// Fail closed: every constant in this package is a code and
				// must be a bare integer literal. An arithmetic expression,
				// iota, or an omitted value would be skipped by a
				// literal-only reader — and a skipped code is exactly one
				// whose missing message and sentinel go unnoticed.
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							t.Errorf("code constant %s has no explicit value; every code must be a bare integer literal", name.Name)
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.INT {
							t.Errorf("code constant %s is not a bare integer literal; the registry cannot be checked", name.Name)
							continue
						}
						codes[name.Name] = lit.Value
					}
				}
			case token.VAR:
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if name.Name == "ErrorMessages" {
							if i >= len(vs.Values) {
								continue
							}
							lit, ok := vs.Values[i].(*ast.CompositeLit)
							if !ok {
								continue
							}
							for _, elt := range lit.Elts {
								kv, ok := elt.(*ast.KeyValueExpr)
								if !ok {
									continue
								}
								key, ok := kv.Key.(*ast.Ident)
								if !ok {
									t.Errorf("%s: ErrorMessages key %v is not a plain code identifier", f.rel, kv.Key)
									continue
								}
								messageKeys[key.Name] = true
								if !isNonEmptyStringLit(kv.Value) {
									emptyMessages = append(emptyMessages, key.Name)
								}
							}
							continue
						}
						// A sentinel is recognised by either its name or its
						// initializer, so neither an Err*-named var built the
						// wrong way NOR a correctly built var under another
						// name can slip past: the first drifts from the map
						// silently, the second dodges the naming contract.
						isSentinelShaped := i < len(vs.Values) && isErrorsNewCall(vs.Values[i])
						if !strings.HasPrefix(name.Name, "Err") {
							if isSentinelShaped {
								badSentinels = append(badSentinels, name.Name+" is a sentinel (errors.New initializer) but is not named Err<Code>")
							}
							continue
						}
						if i >= len(vs.Values) {
							badSentinels = append(badSentinels, name.Name+" is not built as errors.New(ErrorMessages[<Code>])")
							continue
						}
						call, ok := vs.Values[i].(*ast.CallExpr)
						if !ok || !isErrorsNew(call.Fun) {
							badSentinels = append(badSentinels, name.Name+" is not built as errors.New(ErrorMessages[<Code>])")
							continue
						}
						arg := sentinelMessageIndex(call)
						if arg == "" {
							badSentinels = append(badSentinels, name.Name+" is not built as errors.New(ErrorMessages[<Code>])")
							continue
						}
						sentinels = append(sentinels, sentinel{name: name.Name, arg: arg})
					}
				}
			}
		}
	}
	if len(codes) == 0 {
		t.Fatal("no code constants found in pkg/errcode; the gate would pass by finding nothing")
	}
	if len(sentinels) == 0 && len(badSentinels) == 0 {
		t.Fatal("no sentinels found in pkg/errcode; the gate would pass by finding nothing")
	}

	byValue := map[string]string{}
	for name, value := range codes {
		if other, dup := byValue[value]; dup {
			t.Errorf("code value %s is declared twice: %s and %s", value, other, name)
		}
		byValue[value] = name
	}
	for name := range codes {
		if !messageKeys[name] {
			t.Errorf("code %s has no ErrorMessages entry — its sentinel (present or future) would carry an empty message", name)
		}
	}
	for key := range messageKeys {
		if _, ok := codes[key]; !ok {
			t.Errorf("ErrorMessages key %s is not a declared code constant", key)
		}
	}
	for _, key := range emptyMessages {
		t.Errorf("ErrorMessages[%s] is not a non-empty string literal", key)
	}
	for _, s := range sentinels {
		if s.name != "Err"+s.arg {
			t.Errorf("sentinel %s is built from ErrorMessages[%s]; the name and the code must agree (want Err%s)", s.name, s.arg, s.arg)
		}
		if _, ok := codes[s.arg]; !ok {
			t.Errorf("sentinel %s references ErrorMessages[%s], which is not a declared code constant", s.name, s.arg)
		}
	}
	for _, msg := range badSentinels {
		t.Error(msg)
	}

	assertFrontendMirrorsAgree(t, codes)
}

// assertFrontendMirrorsAgree holds the frontend's two numeric mirrors of this
// registry to the backend declarations. The UI branches on exact code values
// (frontend/src/api/errcodes.ts) and localizes by numeric key
// (frontend/src/locales/*/errcodes.ts), so renumbering a backend code that
// the frontend consumes must be a red test here — not a UI branch that
// quietly stops matching while CI stays green.
func assertFrontendMirrorsAgree(t *testing.T, codes map[string]string) {
	t.Helper()
	root := repoRoot(t)
	valueSet := map[string]bool{}
	normalized := map[string]string{} // lowercased Go const name -> literal value
	for name, value := range codes {
		valueSet[value] = true
		normalized[strings.ToLower(name)] = value
	}

	apiData, err := os.ReadFile(filepath.Join(root, "frontend", "src", "api", "errcodes.ts"))
	if err != nil {
		t.Fatalf("reading frontend api errcodes mirror: %v", err)
	}
	// Anchored to the end of the line: `= 10003 + 1` must be a violation,
	// not a constant recorded under the wrong value. Any exported const that
	// is not a bare integer literal is flagged rather than skipped.
	constRe := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = (\d+)\r?$`)
	anyConstRe := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = .*$`)
	consts := constRe.FindAllStringSubmatch(string(apiData), -1)
	if len(consts) == 0 {
		t.Fatal("no exported numeric constants found in frontend/src/api/errcodes.ts; the mirror check would pass by finding nothing")
	}
	strict := map[string]bool{}
	for _, m := range consts {
		strict[m[1]] = true
	}
	for _, m := range anyConstRe.FindAllStringSubmatch(string(apiData), -1) {
		if !strict[m[1]] {
			t.Errorf("frontend constant %s is not a bare integer literal — the mirror cannot be checked", m[1])
		}
	}
	for _, m := range consts {
		tsName, tsValue := m[1], m[2]
		goValue, ok := normalized[strings.ToLower(strings.ReplaceAll(tsName, "_", ""))]
		if !ok {
			t.Errorf("frontend constant %s mirrors no declared backend code constant", tsName)
			continue
		}
		if goValue != tsValue {
			t.Errorf("frontend constant %s = %s, but the backend constant it mirrors is %s", tsName, tsValue, goValue)
		}
	}

	localeFiles, _ := filepath.Glob(filepath.Join(root, "frontend", "src", "locales", "*", "errcodes.ts"))
	if len(localeFiles) == 0 {
		t.Fatal("no locale errcodes.ts files found; the mirror check would pass by finding nothing")
	}
	keyRe := regexp.MustCompile(`(?m)^\s*(\d+):`)
	for _, lf := range localeFiles {
		data, err := os.ReadFile(lf)
		if err != nil {
			t.Fatalf("reading %s: %v", lf, err)
		}
		keys := keyRe.FindAllStringSubmatch(string(data), -1)
		if len(keys) == 0 {
			t.Errorf("%s: no numeric keys found; the mirror check saw nothing", lf)
		}
		for _, k := range keys {
			if !valueSet[k[1]] {
				rel, _ := filepath.Rel(root, lf)
				t.Errorf("%s: message keyed %s maps to no declared backend code", filepath.ToSlash(rel), k[1])
			}
		}
	}
}

// isNonEmptyStringLit reports whether expr is a string literal whose DECODED
// value is non-empty — an empty raw-string literal (two backticks) is still
// empty, whatever its source spelling.
func isNonEmptyStringLit(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s != ""
}

// isErrorsNewCall reports whether expr is a call to errors.New.
func isErrorsNewCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && isErrorsNew(call.Fun)
}

// isErrorsNew matches the errors.New selector.
func isErrorsNew(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "errors" && sel.Sel.Name == "New"
}

// sentinelMessageIndex returns the code identifier X when call is exactly
// errors.New(ErrorMessages[X]), "" otherwise.
func sentinelMessageIndex(call *ast.CallExpr) string {
	if len(call.Args) != 1 {
		return ""
	}
	idx, ok := call.Args[0].(*ast.IndexExpr)
	if !ok {
		return ""
	}
	m, ok := idx.X.(*ast.Ident)
	if !ok || m.Name != "ErrorMessages" {
		return ""
	}
	key, ok := idx.Index.(*ast.Ident)
	if !ok {
		return ""
	}
	return key.Name
}
