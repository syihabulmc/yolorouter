// Shared skip-reason code → i18n key map. Mirrors the SkipReason enum in
// internal/compress/result.go. Used by both CostOptimizationPage and the
// request-log detail page so the two views render identical labels for the
// same skip code. Unknown codes fall back to a generic "other" label with
// the raw code in parentheses for debuggability.
export const SKIP_REASON_KEYS: Record<string, string> = {
  no_live_zone: 'costOptimization.skipReasonNoLiveZone',
  no_matching_compressor: 'costOptimization.skipReasonNoMatchingCompressor',
  no_effective_replacement: 'costOptimization.skipReasonNoEffectiveReplacement',
  total_size_exceeded: 'costOptimization.skipReasonTotalSizeExceeded',
  timeout: 'costOptimization.skipReasonTimeout',
  parse_error: 'costOptimization.skipReasonParseError',
  fail_open: 'costOptimization.skipReasonFailOpen',
}
