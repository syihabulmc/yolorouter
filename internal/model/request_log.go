// RequestLog — one row per gateway business
// request. Schema lives in
// migrations/{sqlite,postgres}/00007_create_request_logs.sql — goose owns
// DDL, GORM here is query-only (no AutoMigrate).
package model

import "time"

// RequestLog is the gateway's per-request audit/cost row. A failover is still
// ONE row — Attempts records how many candidates were tried. The
// query/filter UI is a separate module; this layer only writes rows.
//
// CostKnown=false means price or token info was missing for this request —
// CostMicros is 0 but must NOT be displayed as "free".
type RequestLog struct {
	ID               uint   `gorm:"column:id;primaryKey" json:"id"`
	RequestID        string `gorm:"column:request_id" json:"request_id"`
	APIKeyID         *uint  `gorm:"column:api_key_id" json:"api_key_id"`
	ModelName        string `gorm:"column:model_name" json:"model_name"`
	ProviderID       *uint  `gorm:"column:provider_id" json:"provider_id"`
	IsStream         bool   `gorm:"column:is_stream" json:"is_stream"`
	StatusCode       int    `gorm:"column:status_code" json:"status_code"`
	InputTokens      int    `gorm:"column:input_tokens" json:"input_tokens"`
	OutputTokens     int    `gorm:"column:output_tokens" json:"output_tokens"`
	CacheWriteTokens int    `gorm:"column:cache_write_tokens" json:"cache_write_tokens"`
	CacheReadTokens  int    `gorm:"column:cache_read_tokens" json:"cache_read_tokens"`
	CostMicros       int64  `gorm:"column:cost_micros" json:"cost_micros"`
	CostKnown        bool   `gorm:"column:cost_known" json:"cost_known"`
	// Cache economics, computed at write time against the candidate's prices.
	// CacheReadSavedMicros is what the cache-read tokens saved versus the input
	// price; CacheWriteExtraMicros is the premium paid to write the cache. Both
	// non-negative; net saving = read saved − write extra (derived by readers).
	CacheReadSavedMicros  int64 `gorm:"column:cache_read_saved_micros" json:"cache_read_saved_micros"`
	CacheWriteExtraMicros int64 `gorm:"column:cache_write_extra_micros" json:"cache_write_extra_micros"`
	// Input-compression accounting, computed when the request passes through
	// the compress stage. TokensSaved/CostSavedMicros are non-negative
	// estimates of the reduction achieved; SkipReason is '' when compression
	// ran, otherwise a short stable code explaining why it was bypassed
	// (e.g. 'too_small', 'unsupported_mime'); CompressorsApplied is a
	// comma-joined list of compressor names that actually fired.
	CompressEstimatedTokensSaved     int       `gorm:"column:compress_estimated_tokens_saved" json:"compress_estimated_tokens_saved"`
	CompressEstimatedCostSavedMicros int64     `gorm:"column:compress_estimated_cost_saved_micros" json:"compress_estimated_cost_saved_micros"`
	CompressSkipReason               string    `gorm:"column:compress_skip_reason" json:"compress_skip_reason"`
	CompressorsApplied               string    `gorm:"column:compressors_applied" json:"compressors_applied"`
	FailReason                       *string   `gorm:"column:fail_reason" json:"fail_reason"`
	Attempts                         int       `gorm:"column:attempts" json:"attempts"`
	AttemptsDetail                   *string   `gorm:"column:attempts_detail" json:"attempts_detail"`
	DurationMs                       int64     `gorm:"column:duration_ms" json:"duration_ms"`
	CreatedAt                        time.Time `gorm:"column:created_at" json:"created_at"`
}

func (RequestLog) TableName() string { return "request_logs" }
