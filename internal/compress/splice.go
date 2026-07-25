package compress

import "sort"

// Replacement describes a byte-range substitution over the original body:
// Range is the half-open interval [Range[0], Range[1]).
type Replacement struct {
	Range       [2]int
	Replacement []byte // already JSON-encoded string literal (json.Marshal of a string)
}

// SpliceBody applies a set of non-overlapping Replacements to originalBody.
// Bytes outside any range are copied verbatim. The whole body is never
// Unmarshal-ed then Marshal-ed back. reps may be passed in any order;
// they are sorted internally by start offset.
func SpliceBody(originalBody []byte, reps []Replacement) []byte {
	if len(reps) == 0 {
		return originalBody
	}
	sorted := make([]Replacement, len(reps))
	copy(sorted, reps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Range[0] < sorted[j].Range[0] })

	out := make([]byte, 0, len(originalBody))
	cursor := 0
	for _, r := range sorted {
		if r.Range[0] < cursor || r.Range[1] > len(originalBody) || r.Range[0] > r.Range[1] {
			// Range is invalid or overlaps a previous one: defensively skip
			// this replacement so the body stays well-formed.
			continue
		}
		out = append(out, originalBody[cursor:r.Range[0]]...)
		out = append(out, r.Replacement...)
		cursor = r.Range[1]
	}
	out = append(out, originalBody[cursor:]...)
	return out
}
