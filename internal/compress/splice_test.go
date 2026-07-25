package compress

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSpliceBodyReplacesRangeOnly(t *testing.T) {
	orig := []byte(`{"a":"KEEP","b":"OLD","c":"KEEP"}`)
	// Replace the "OLD" value of b with "NEW". Range is computed by hand
	// to cover the value including its surrounding quotes: [start, end).
	start := bytes.Index(orig, []byte(`"OLD"`))
	rep := Replacement{Range: [2]int{start, start + len(`"OLD"`)}, Replacement: mustJSON("NEW")}
	out := SpliceBody(orig, []Replacement{rep})
	if string(out) != `{"a":"KEEP","b":"NEW","c":"KEEP"}` {
		t.Fatalf("expected only the target range to change, got %s", out)
	}
}

func TestSpliceBodyNoRepIsIdentity(t *testing.T) {
	orig := []byte(`{"x":1}`)
	out := SpliceBody(orig, nil)
	if !bytes.Equal(out, orig) {
		t.Fatal("with no replacements the output must be bytes.Equal to the input")
	}
}

func mustJSON(s string) []byte { b, _ := json.Marshal(s); return b }
