package compress

import (
	"regexp"
	"strings"
)

var (
	// diffGitRe matches only lines that start with "diff --git " — the
	// unambiguous marker of a git diff (avoids mistaking isolated ---/@@ lines).
	diffGitRe    = regexp.MustCompile(`^diff --git `)
	searchLineRe = regexp.MustCompile(`^[^\s:]+[./][^\s:]*:\d+:`)
)

// ContentType identifies the detected content category.
type ContentType int

const (
	ContentPlainText     ContentType = iota
	ContentBuildOutput               // go test / build log
	ContentGitDiff                   // git diff output
	ContentSearchResults             // grep / rg search output
)

// logPatterns are the anchors used to recognize build/test output.
// The set is compiled once at package init.
var logPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ERROR|FAIL|FAILED|FATAL|CRITICAL)\b`),
	regexp.MustCompile(`(?i)\b(WARN|WARNING)\b`),
	regexp.MustCompile(`^\s*(PASS|FAIL|SKIP)\b`),
	regexp.MustCompile(`^=== RUN\b`),
	regexp.MustCompile(`^--- (PASS|FAIL|SKIP):`),
	regexp.MustCompile(`^ok\s`),
	regexp.MustCompile(`^\?\s`),
	regexp.MustCompile(`Traceback \(most recent call last\)`),
}

// tryDetectDiff returns ContentGitDiff when at least one line begins with
// the "diff --git " marker.
func tryDetectDiff(lines []string) (ContentType, bool) {
	for _, ln := range lines {
		if diffGitRe.MatchString(ln) {
			return ContentGitDiff, true
		}
	}
	return ContentPlainText, false
}

// tryDetectSearch recognizes grep/rg output: the fraction of non-empty lines
// matching the search-line pattern must reach the 1/3 threshold.
func tryDetectSearch(lines []string) (ContentType, bool) {
	var matched, nonEmpty int
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		nonEmpty++
		if searchLineRe.MatchString(ln) {
			matched++
		}
	}
	if nonEmpty == 0 || matched == 0 {
		return ContentPlainText, false
	}
	// Threshold: >= 33% of non-empty lines must look like search hits.
	if float64(matched)/float64(nonEmpty) >= 1.0/3.0 {
		return ContentSearchResults, true
	}
	return ContentPlainText, false
}

// detectContentType classifies content by priority:
// GitDiff -> BuildOutput -> SearchResults -> PlainText.
// A single splitLinesCapped(500) slice is reused across stages to avoid
// scanning the input more than once.
func detectContentType(content string) ContentType {
	if content == "" {
		return ContentPlainText
	}
	lines := splitLinesCapped(content, 500)

	if ct, ok := tryDetectDiff(lines); ok {
		return ct
	}

	// BuildOutput is checked before SearchResults so that Go compiler
	// diagnostics (file.go:N:) are not misclassified as search hits.
	buildLines := lines
	if len(buildLines) > 200 {
		buildLines = buildLines[:200]
	}
	var matched, nonEmpty int
	for _, ln := range buildLines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		nonEmpty++
		for _, re := range logPatterns {
			if re.MatchString(ln) {
				matched++
				break
			}
		}
	}
	if nonEmpty > 0 && matched > 0 {
		confidence := 0.3 + float64(matched)/float64(nonEmpty)*0.5
		if confidence >= 0.5 {
			return ContentBuildOutput
		}
	}

	searchLines := lines
	if len(searchLines) > 100 {
		searchLines = searchLines[:100]
	}
	if ct, ok := tryDetectSearch(searchLines); ok {
		return ct
	}

	return ContentPlainText
}

// splitLinesCapped splits s on '\n' and returns at most the first n lines,
// so very large inputs are not scanned in full.
func splitLinesCapped(s string, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(s) && len(out) < n; i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if len(out) < n && start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
