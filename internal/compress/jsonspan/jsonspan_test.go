package jsonspan

import "testing"

func TestParseStringReturnsDecodedContentAndLiteralSpan(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		pos     int
		decoded string
		literal string
	}{
		{"plain", `{"k":"value"}`, 5, "value", `"value"`},
		{"escaped quote", `{"k":"a\"b"}`, 5, `a"b`, `"a\"b"`},
		{"escaped backslash then quote", `{"k":"a\\"}`, 5, `a\`, `"a\\"`},
		{"unicode escape", `{"k":"é"}`, 5, "é", `"é"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Scanner{Data: []byte(tc.data), Pos: tc.pos}
			decoded, start, end := p.ParseString()
			if decoded != tc.decoded {
				t.Errorf("decoded = %q, want %q", decoded, tc.decoded)
			}
			if got := tc.data[start:end]; got != tc.literal {
				t.Errorf("Data[start:end] = %q, want the quoted literal %q", got, tc.literal)
			}
			if p.Pos != end {
				t.Errorf("Pos = %d after parse, want the literal's end %d", p.Pos, end)
			}
		})
	}
}

func TestSkipValueStopsAtTheNextDelimiter(t *testing.T) {
	cases := []struct {
		name string
		data string
		rest string // what remains from Pos after the skip
	}{
		{"string", `"abc",1]`, `,1]`},
		{"number", `12.5e3,x`, `,x`},
		{"true", `true}`, `}`},
		{"null", `null]`, `]`},
		{"object with nested string braces", `{"a":"}{","b":[1]},tail`, `,tail`},
		{"array with nested brackets", `[[1,2],[3]],tail`, `,tail`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Scanner{Data: []byte(tc.data)}
			p.SkipValue()
			if got := tc.data[p.Pos:]; got != tc.rest {
				t.Errorf("after SkipValue rest = %q, want %q", got, tc.rest)
			}
		})
	}
}

// TestSkipContainerIgnoresBracesInsideStrings pins the property the whole
// scanner exists for: pairing must be structural, not textual. A '}' inside
// a string literal is content, and counting it would cut the container short
// — every span reported after that point would be shifted garbage.
func TestSkipContainerIgnoresBracesInsideStrings(t *testing.T) {
	data := `{"a":"}}}","b":{"c":"]"}} tail`
	p := &Scanner{Data: []byte(data)}
	p.SkipContainer('{', '}')
	if got := data[p.Pos:]; got != " tail" {
		t.Fatalf("after SkipContainer rest = %q, want %q", got, " tail")
	}
}

func TestSeekTopLevelArray(t *testing.T) {
	t.Run("hit leaves Pos on the opening bracket", func(t *testing.T) {
		data := `{"model":"m","messages":[{"role":"user"}]}`
		p := &Scanner{Data: []byte(data)}
		if !p.SeekTopLevelArray("messages") {
			t.Fatal("SeekTopLevelArray(messages) = false on a body that has it")
		}
		if data[p.Pos] != '[' {
			t.Fatalf("Pos is on %q, want '['", data[p.Pos])
		}
	})
	t.Run("miss on absent key", func(t *testing.T) {
		p := &Scanner{Data: []byte(`{"model":"m"}`)}
		if p.SeekTopLevelArray("messages") {
			t.Fatal("SeekTopLevelArray reported a hit for a key that is not there")
		}
	})
	t.Run("non-array value is a miss", func(t *testing.T) {
		p := &Scanner{Data: []byte(`{"messages":"not-an-array"}`)}
		if p.SeekTopLevelArray("messages") {
			t.Fatal("SeekTopLevelArray reported a hit for a non-array value")
		}
	})
	t.Run("only top level is searched", func(t *testing.T) {
		p := &Scanner{Data: []byte(`{"outer":{"messages":[1]}}`)}
		if p.SeekTopLevelArray("messages") {
			t.Fatal("SeekTopLevelArray found a nested key; it must search the top level only")
		}
	})
}

func TestWalkObjectForKeyVisitsEveryOccurrenceAndSkipsTheRest(t *testing.T) {
	data := `{"a":1,"content":"x","b":[true],"content":"y"}`
	p := &Scanner{Data: []byte(data)}
	var seen []string
	p.WalkObjectForKey("content", func() {
		s, _, _ := p.ParseString()
		seen = append(seen, s)
	})
	if len(seen) != 2 || seen[0] != "x" || seen[1] != "y" {
		t.Fatalf("visited %v, want [x y]", seen)
	}
	if p.Pos != len(data) {
		t.Fatalf("Pos = %d after the walk, want the object consumed to %d", p.Pos, len(data))
	}
}
