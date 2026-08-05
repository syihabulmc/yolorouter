// Package contentinspect recognises upstream refusals that judged the payload's
// content rather than its shape.
//
// Content-safety rejections are the one class of 4xx that another candidate can
// plausibly serve. A malformed request fails identically everywhere, so failing
// over only burns the candidate chain — but a provider's input inspection is a
// property of THAT provider, not of the request: the same payload routinely
// passes at a provider whose moderation is tuned differently or absent
// entirely.
//
// A representative refusal, verbatim from an OpenAI-compatible upstream:
//
//	{"error":{"message":"Input data may contain inappropriate content.",
//	          "type":"invalid_request_error","code":"data_inspection_failed"}}
//
// The payload that triggered it was an assistant's system prompt that
// enumerated public figures' names so it could handle questions about them
// carefully. The upstream's inspector saw the name list and refused before the
// model ever ran, so every call from that client failed on that provider while
// other candidates in the same chain would have served it. Classified by status
// code alone the 400 is terminal, and no failover is ever attempted.
//
// The cost of a false positive is bounded and self-correcting: at worst the
// request walks candidates that reject it the same way, and the refusal still
// reaches the client. There is no path where a false positive turns a failure
// into a wrong success.
//
// This package reports what it recognised and stops there. Whether a refusal is
// worth another candidate, whether the budget allows it, and what the caller is
// ultimately told are not decisions it is able to express — which is why it has
// no view of the exchange at all.
package contentinspect

import (
	"context"
	"strings"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// View is empty on purpose: this inspector judges an upstream response and
// nothing else. It reads no exchange state, so there is nothing for it to be
// coupled to.
type View interface{}

// Inspector recognises content-safety refusals.
type Inspector struct{}

// New returns an inspector. It is stateless; one instance serves every request.
func New() Inspector { return Inspector{} }

// Name identifies the reporter in the audit trail.
func (Inspector) Name() string { return "content_inspection" }

// ObserveUpstreamError reports a refusal when the response is one. Reporting nothing
// is the normal case and means "I recognised nothing here", not "this response
// is fine" — several observers see the same response and none of them has the
// whole picture.
func (Inspector) ObserveUpstreamError(_ context.Context, _ View, up fact.Upstream, sink fact.Sink) {
	if !IsRefusal(up.StatusCode, string(up.Body)) {
		return
	}
	sink.Report(fact.Fact{
		Kind:   fact.KindPayloadRefused,
		Status: up.StatusCode,
		Detail: "upstream inspected the payload and refused it",
	})
}

// isRefusalStatus reports whether a status is one upstreams actually use for an
// input-moderation refusal.
//
// 422 and 451 are in the set alongside the 400 that motivated this: an upstream
// that models a refusal as "well-formed but unprocessable" or reaches for the
// legal-reasons code is describing the same event, and which of the three a
// vendor picks is a house style, not a semantic difference.
func isRefusalStatus(statusCode int) bool {
	switch statusCode {
	case 400, 403, 422, 451:
		return true
	default:
		return false
	}
}

// refusalCodes are vendor error codes that mean "moderation refused this
// payload" on their own, with no corroborating phrase needed. Matched as
// lowercase substrings of the raw error body rather than parsed out of a
// specific JSON field, because the field name differs per vendor (code /
// error_code / type / innererror.code) and several nest it arbitrarily deep.
var refusalCodes = []string{
	"data_inspection_failed",
	"datainspectionfailed",
	"content_filter",
	"contentfilter",
	"content_policy_violation",
	"sensitivecontentdetected",
	"content_censor",
	"risk_control",
}

// refusalSubjects name WHAT was judged. A moderation verdict always refers to
// the payload; a phrase like "unsafe" with no such subject is far more likely to
// be about something else (an unsafe parameter combination).
//
// The non-English entries here and in refusalVerdicts below are match data, not
// prose: several upstreams report moderation only in their own language, so the
// needles have to be written in the language the haystack arrives in. Dropping
// them would silently disable detection for every such provider.
//
// "message" is deliberately absent even though it is the obvious synonym:
// virtually every vendor's error envelope has a "message" field, so accepting it
// would make the subject half of the test always true and collapse the
// two-condition design into a bare verdict match.
var refusalSubjects = []string{
	"content", "input", "prompt", "request data",
	"内容", "输入", "提示词", "文本", "消息",
}

// refusalVerdicts name the JUDGMENT. Deliberately excludes generic rejection
// words ("invalid", "not allowed", "unsupported") that dominate ordinary
// schema/parameter 400s — those must keep failing fast on the first candidate.
// For the same reason it says "policy violation" rather than a bare "violat"
// stem, which would also match "input violates the schema".
var refusalVerdicts = []string{
	"inappropriate", "unsafe", "sensitive", "prohibited",
	"moderation", "censor", "exists risk", "blocked by",
	"policy violation", "safety policy", "safety system",
	"敏感", "不安全", "违规", "不合规", "审核不通过", "安全策略", "涉及风险",
}

// IsRefusal reports whether a non-2xx upstream response is an input-moderation
// refusal — a verdict about the payload's content — rather than a fault in the
// request's shape.
//
// It must stay conservative: matching a plain schema error would walk the whole
// candidate chain for a request every candidate rejects just as fast.
func IsRefusal(statusCode int, errBody string) bool {
	if !isRefusalStatus(statusCode) || errBody == "" {
		return false
	}
	lower := strings.ToLower(errBody)
	for _, code := range refusalCodes {
		if strings.Contains(lower, code) {
			return true
		}
	}
	// Fallback for vendors that report moderation in prose only (several return
	// a numeric code plus a sentence). Requires a subject AND a verdict so
	// ordinary 400s don't match.
	hasSubject := false
	for _, s := range refusalSubjects {
		if strings.Contains(lower, s) {
			hasSubject = true
			break
		}
	}
	if !hasSubject {
		return false
	}
	for _, v := range refusalVerdicts {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}
