// Package jsonspan is a lightweight offset-preserving JSON scanner: it walks
// raw JSON bytes without building a tree, and every position it reports is a
// span into the original buffer, so a caller can splice edited regions back
// into the document byte-for-byte.
//
// The scanner is deliberately lenient — it does not validate syntax. A caller
// that needs strict legality gates with json.Valid first; from then on the
// scanner's job is navigation, not judgement.
package jsonspan

import "encoding/json"

// Scanner walks Data from Pos. Both fields are exported so a caller can seed
// a scanner mid-document (resuming at a span an earlier pass reported) and
// read the position a primitive stopped at.
type Scanner struct {
	Data []byte
	Pos  int
}

// SkipWS advances Pos past JSON whitespace.
func (p *Scanner) SkipWS() {
	for p.Pos < len(p.Data) {
		switch p.Data[p.Pos] {
		case ' ', '\t', '\n', '\r':
			p.Pos++
		default:
			return
		}
	}
}

// ParseString reads a JSON string starting at Data[Pos] (which must be the
// opening quote). It advances Pos past the closing quote and returns the
// decoded content plus the (start, end) offsets such that Data[start:end] is
// the quoted literal.
func (p *Scanner) ParseString() (string, int, int) {
	start := p.Pos
	j := p.Pos + 1
	for j < len(p.Data) {
		c := p.Data[j]
		if c == '\\' {
			j += 2
			continue
		}
		if c == '"' {
			j++
			break
		}
		j++
	}
	end := j
	var decoded string
	if err := json.Unmarshal(p.Data[start:end], &decoded); err != nil {
		// Fallback: strip the surrounding quotes and take the raw bytes.
		// json.Valid is expected to have passed upstream, so this is defensive.
		if end-start >= 2 {
			decoded = string(p.Data[start+1 : end-1])
		}
	}
	p.Pos = end
	return decoded, start, end
}

// SkipValue advances Pos past one JSON value (string, object, array, number,
// boolean, or null).
func (p *Scanner) SkipValue() {
	p.SkipWS()
	if p.Pos >= len(p.Data) {
		return
	}
	switch p.Data[p.Pos] {
	case '"':
		p.ParseString()
	case '{':
		p.SkipContainer('{', '}')
	case '[':
		p.SkipContainer('[', ']')
	default:
		for p.Pos < len(p.Data) {
			switch p.Data[p.Pos] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return
			}
			p.Pos++
		}
	}
}

// SkipContainer advances Pos past a matching open/close container (Pos must
// point at open). Bytes inside string literals do not participate in pairing.
func (p *Scanner) SkipContainer(open, close byte) {
	depth := 0
	for p.Pos < len(p.Data) {
		c := p.Data[p.Pos]
		switch c {
		case '"':
			p.ParseString()
		case open:
			depth++
			p.Pos++
		case close:
			depth--
			p.Pos++
			if depth == 0 {
				return
			}
		default:
			p.Pos++
		}
	}
}

// SeekTopLevelArray looks up key in the top-level object and, on hit, leaves
// Pos on the '[' that opens its array value. Returns true on hit.
func (p *Scanner) SeekTopLevelArray(key string) bool {
	p.SkipWS()
	if p.Pos >= len(p.Data) || p.Data[p.Pos] != '{' {
		return false
	}
	p.Pos++
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return false
		}
		if p.Data[p.Pos] == '}' {
			return false
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		k, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		if k == key {
			return p.Pos < len(p.Data) && p.Data[p.Pos] == '['
		}
		p.SkipValue()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}

// WalkObjectForKey scans one object (Pos must point at '{' or leading
// whitespace) and invokes visit with the scanner positioned at targetKey's
// value each time the key occurs; every other member is skipped. visit must
// consume the value it is handed — leaving Pos inside it would desynchronize
// the walk.
func (p *Scanner) WalkObjectForKey(targetKey string, visit func()) {
	p.SkipWS()
	if p.Pos >= len(p.Data) || p.Data[p.Pos] != '{' {
		return
	}
	p.Pos++ // consume '{'
	for {
		p.SkipWS()
		if p.Pos >= len(p.Data) {
			return
		}
		if p.Data[p.Pos] == '}' {
			p.Pos++
			return
		}
		if p.Data[p.Pos] != '"' {
			p.Pos++
			continue
		}
		key, _, _ := p.ParseString()
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ':' {
			p.Pos++
		}
		p.SkipWS()
		if key == targetKey {
			visit()
		} else {
			p.SkipValue()
		}
		p.SkipWS()
		if p.Pos < len(p.Data) && p.Data[p.Pos] == ',' {
			p.Pos++
		}
	}
}
