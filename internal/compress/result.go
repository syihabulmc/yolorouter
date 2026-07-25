// Package compress implements relay-layer live-zone input compression.
package compress

import "time"

// SkipReason describes why the whole compression pass was skipped.
type SkipReason string

const (
	SkipReasonNoLiveZone             SkipReason = "no_live_zone"
	SkipReasonNoMatchingCompressor   SkipReason = "no_matching_compressor"   // live blocks exist but no compressor matches their content type (compressorsFor=nil) — a real coverage gap
	SkipReasonNoEffectiveReplacement SkipReason = "no_effective_replacement" // live blocks and compressors exist, but every block was too small, did not shrink, or errored
	SkipReasonTotalSizeExceeded      SkipReason = "total_size_exceeded"
	SkipReasonTimeout                SkipReason = "timeout"
	SkipReasonParseError             SkipReason = "parse_error"
	SkipReasonFailOpen               SkipReason = "fail_open"
)

// CompressResult holds the outcome of a compression pass. When Skipped is true
// the new body is bytes.Equal to the original body.
type CompressResult struct {
	EstimatedTokensSaved int      // (sum of original chars minus compressed chars) / 4
	CompressorsApplied   []string // names of compressors that produced replacements
	BlocksModified       int
	Skipped              bool
	SkipReason           SkipReason
}

// CompressOptions configures a compression pass.
type CompressOptions struct {
	MinBlockBytes    int           // blocks smaller than this are left untouched
	MaxLiveZoneBytes int           // total live-zone byte budget; exceeding it skips the whole request
	Timeout          time.Duration // wall-clock budget for the whole pass
}

// DefaultOptions returns the built-in defaults.
func DefaultOptions() CompressOptions {
	return CompressOptions{
		MinBlockBytes:    512,
		MaxLiveZoneBytes: 10 * 1024 * 1024,
		Timeout:          200 * time.Millisecond,
	}
}
