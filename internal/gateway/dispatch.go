package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
)

// buildUpstreamBody constructs the upstream request body and URL for one
// candidate. When the ingress and egress speak the same wire protocol, the
// caller's body is forwarded unchanged apart from the model field
// (passthrough — no IR round-trip, so vendor-specific fields the IR doesn't
// model are preserved). Otherwise the body is decoded into the IR via the
// ingress decoder and re-encoded via the egress encoder. It does not build
// the *http.Request itself — that (and the key-specific auth header via
// SetupRequest) is per-key work left to attemptOne (relay.go), since this
// transform is identical across every key attempt for a given candidate.
func (s *Service) buildUpstreamBody(
	rc *Exchange,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
) ([]byte, string, resolved, error) {
	if egress == nil {
		return nil, "", resolved{}, fmt.Errorf("gateway: buildUpstreamBody requires a non-nil egress decision")
	}

	providerModelName := rc.candidate.ProviderModelName
	egressCodecs := codecsFor(egress.Protocol)

	var outBody []byte
	if egress.Passthrough {
		rewritten, err := passthroughRequestBody(egress.Protocol, rc.EffectiveRequestBody(), providerModelName)
		if err != nil {
			return nil, "", resolved{}, fmt.Errorf("rewrite model field: %w", err)
		}
		outBody = rewritten
	} else {
		dec := codecsFor(ingress).RequestDecoder
		ir, err := dec.DecodeRequest(json.RawMessage(rc.EffectiveRequestBody()), providerModelName, rc.isStream)
		if err != nil {
			return nil, "", resolved{}, fmt.Errorf("decode ingress request (%s): %w", ingress, err)
		}
		encoded, err := egressCodecs.RequestEncoder.EncodeRequest(ir)
		if err != nil {
			return nil, "", resolved{}, fmt.Errorf("encode egress request (%s): %w", egress.Protocol, err)
		}
		outBody = []byte(encoded)
	}

	if egress.Protocol == protocols.ProtocolOpenAI {
		injected, err := EnsureStreamUsageInjection(outBody, rc.isStream, rc.wantsStreamUsage)
		if err != nil {
			return nil, "", resolved{}, fmt.Errorf("inject stream usage: %w", err)
		}
		outBody = injected
	}

	// Rewriters run after stream-usage injection so they operate on the final
	// egress-encoded body, in the order assembly gave them. A rewriter that
	// refuses the body comes back as a verdict for the caller to act on, not as
	// an error here: what a refusal costs the request is the table's call.
	outBody, verdict := s.rewriteEgress(rc.requestCtx, rc, egress.Protocol, outBody)

	path := egressCodecs.RequestEncoder.EgressPath(providerModelName, rc.isStream)
	url := protocols.JoinUpstreamURL(egress.BaseURL, path, egress.Protocol)
	if egress.Protocol == protocols.ProtocolGemini && rc.isStream {
		url = strings.Replace(url, ":generateContent", ":streamGenerateContent?alt=sse", 1)
	}

	return outBody, url, verdict, nil
}

// passthroughRequestBody prepares the same-protocol passthrough request body
// bound for the upstream, branching by egress protocol on how (or whether)
// the caller's external model name needs to become the candidate's
// provider_model_name in the outgoing bytes.
//
// OpenAI, Claude, and Responses requests all carry "model" as a top-level
// body field, so rewriteModelField swaps in the provider model name there,
// same as always. A native Gemini request has no such field at all — the
// model only ever appears in the URL path (EgressPath builds
// /v1beta/models/{providerModelName}:generateContent, applied by the caller
// right after this function returns) — so rc.requestBody is forwarded
// completely unchanged: injecting a top-level "model" key would add a field
// the real Gemini API's protojson-based request parser rejects as unknown,
// turning a passthrough request into a hard failure.
func passthroughRequestBody(egressProtocol protocols.ProtocolID, body []byte, providerModelName string) ([]byte, error) {
	if egressProtocol == protocols.ProtocolGemini {
		return body, nil
	}
	return rewriteModelField(body, providerModelName)
}

// modelOverrideResponseEncoder decorates a protocols.ResponseEncoder to force
// IRResponse.Model to the caller-facing external model name before encoding.
// The IR decode step (egress side) fills Model from whatever the upstream
// itself reported — the candidate's provider_model_name for every real
// provider — which must never reach the client; the passthrough path
// achieves the same end via RewriteNonStreamResponse's model-field rewrite.
type modelOverrideResponseEncoder struct {
	inner protocols.ResponseEncoder
	model string
}

func (e modelOverrideResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	if resp != nil && e.model != "" {
		resp.Model = e.model
	}
	return e.inner.EncodeResponse(resp)
}

// modelOverrideStreamEncoder decorates a protocols.StreamEncoder to force
// every DeltaMessageStart's Model to the caller-facing external model name
// before encoding — the streaming counterpart of modelOverrideResponseEncoder
// above. The egress stream decoder fills DeltaMessageStart.Model from
// whatever the upstream itself reported (the candidate's
// provider_model_name), which must never reach the client.
type modelOverrideStreamEncoder struct {
	inner protocols.StreamEncoder
	model string
}

func (e modelOverrideStreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	if e.model != "" {
		for i, d := range deltas {
			if ms, ok := d.(protocols.DeltaMessageStart); ok {
				ms.Model = e.model
				deltas[i] = ms
			}
		}
	}
	return e.inner.EncodeDeltas(deltas)
}

func (e modelOverrideStreamEncoder) EncodeDone() []protocols.SSEEvent {
	return e.inner.EncodeDone()
}

func (e modelOverrideStreamEncoder) Usage() protocols.IRUsage {
	return e.inner.Usage()
}

// processDispatchResponseStream routes a 2xx streaming upstream response to
// the client for one attempt. It is attemptOne's (relay.go) stream
// counterpart to processDispatchResponseNonStream, called only after
// SendUpstreamRequest returned a 2xx status for a streaming request.
//
// Passthrough (egress.Passthrough — the only live path today, see
// processDispatchResponseNonStream's doc comment for why) calls
// dispatchPassthroughStream: byte-for-byte the same SSE pump, model rewrite,
// [DONE]-tracking, and disconnect/partial-completion lifecycle the
// pre-IR gateway had, which the existing regression suite pins.
//
// Cross-protocol responses go through protocols.IRStreamRelay (or
// IRStreamRelayJSONLines for a Gemini egress, which speaks JSON Lines rather
// than SSE) — decode via the egress protocol's StreamDecoder, re-encode via
// the ingress protocol's StreamEncoder (wrapped so emitted chunks carry the
// external model name, mirroring the non-stream branch). This path is not
// production-reachable until a later milestone: every provider's
// provider_type is openai today, so Negotiate (negotiate.go) always echoes
// the ingress protocol.
//
// Like the passthrough branch, the caller-facing stream capture file
// (streamBodyFile) is opened before the relay call and closed on every exit
// path below: protocols.IRStreamRelay/IRStreamRelayJSONLines append the
// caller-facing (post-re-encode) bytes actually written to the client via
// rc.AppendResponse (protocols.UpstreamBuffer) at the same point they write
// to c.Writer, so the capture file ends up byte-for-byte identical to what
// the client received — never the raw upstream lines (rc.AppendUpstream is a
// deliberate no-op for this reason, see ir_bridge.go).
func (s *Service) processDispatchResponseStream(
	c *gin.Context,
	rc *Exchange,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
	cand model.ModelCandidate,
	provider *model.Provider,
	pk model.ProviderKey,
	resp *http.Response,
	start time.Time,
) attemptResult {
	if egress.Passthrough {
		return s.dispatchPassthroughStream(c, rc, egress.Protocol, cand, provider, pk, resp, start)
	}

	// Now committed to this 2xx candidate — a stream request never
	// populates these two fields itself (its response lives in the stream
	// capture file instead), but a prior failed candidate may have stashed
	// a stale error body in them.
	rc.clearResponseBodies()

	// Mirrors dispatchPassthroughStream's openStreamBodyFile/closeStreamBodyFile
	// pairing: the file must stay open past the relay call so
	// writeStreamErrorEvent's mid-stream error frame (below) can still be
	// appended to it, so the close is deferred to the end of this function
	// rather than happening right after the relay call returns.
	openStreamBodyFile(c, rc)
	defer closeStreamBodyFile(rc)

	egressDecoder := codecsFor(egress.Protocol).NewStreamDecoder()
	innerStreamEncoder := codecsFor(ingress).NewStreamEncoder()
	// Only an OpenAI ingress has a stream_options.include_usage knob; wire
	// it onto the concrete chat encoder before it gets wrapped below, since
	// modelOverrideStreamEncoder only exposes the protocols.StreamEncoder
	// interface (no IncludeUsage field) to the rest of this function.
	if ingress == protocols.ProtocolOpenAI {
		if chatEncoder, ok := innerStreamEncoder.(*chat.StreamEncoder); ok {
			chatEncoder.IncludeUsage = rc.wantsStreamUsage
		}
	}
	ingressEncoder := modelOverrideStreamEncoder{
		inner: innerStreamEncoder,
		model: rc.originalModel,
	}

	// protocols.IRStreamRelay/IRStreamRelayJSONLines defer the SSE response
	// headers until the first encoded event is actually about to be
	// written, mirroring the passthrough pump (passthroughStreamToClient):
	// a pre-first-event upstream failure (empty response, malformed first
	// frame, a read error before any event ever reaches the client) returns
	// an error WITHOUT ever having written headers, so this candidate can
	// still fail over to a healthy one. rc.MarkFirstByteSent is therefore
	// NOT called up front here — it is wired as the relay's onFirstChunk
	// callback instead, so it only flips once a response has actually been
	// committed to the client (mirroring the lifecycle table: "received
	// upstream response, first chunk not yet sent to caller -> failover
	// allowed").
	onFirstChunk := func() { rc.MarkFirstByteSent() }

	var usage *protocols.IRUsage
	var err error
	if egress.Protocol == protocols.ProtocolGemini {
		usage, err = protocols.IRStreamRelayJSONLines(c, resp, egressDecoder, ingressEncoder, rc, onFirstChunk, nil)
	} else {
		usage, err = protocols.IRStreamRelay(c, resp, egressDecoder, ingressEncoder, rc, onFirstChunk, nil)
	}
	// Preserve partial usage even on an error path, mirroring
	// dispatchPassthroughStream. irUsageToUsage nils out an all-zero usage
	// (unknown, not zero) even though usage itself is never a nil pointer
	// here.
	rc.usage = irUsageToUsage(usage)
	// If the relay never emitted a single encoded event (a pre-first-event
	// failure below), the capture file (if any) is empty — remove it so the
	// detail page never shows an empty "stream body" link, mirroring
	// dispatchPassthroughStream's identical call.
	removeEmptyStreamBodyFile(c, rc)

	if err == nil {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
		s.finalize(rc, http.StatusOK, "", start)
		return attemptSuccess
	}
	if rc.firstByteSent {
		// Mid-stream failure after the response headers/first bytes went out
		// — can't switch, can't change HTTP status; same handling as
		// dispatchPassthroughStream's mid-stream branch.
		return s.finalizeClientWriteOrPartial(c, rc, err, cand, provider, pk, resp, start)
	}
	// Pre-first-byte failure: the IR relay returned before ever writing
	// headers to the client (onFirstChunk/rc.MarkFirstByteSent never
	// fired), so nothing has been committed yet and this candidate can
	// still fail over to the next one — mirrors
	// dispatchPassthroughStream's pre-first-byte failover branch.
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream start: "+err.Error()))
	return attemptNextCandidate
}

// finalizeClientWrite records a downstream Write/Flush failure (already
// classified by the caller — either via isClientWriteError or a direct
// client-disconnect check) as attempt outcome AttemptConnError and settles
// the request as 499 client_write_timeout. Shared by every call site that has
// already committed a response to the client (status/headers on the wire)
// and therefore cannot fail over: finalizeClientWriteOrPartial (mid-stream,
// both the IR and passthrough stream paths), processDispatchResponseNonStream
// (cross-protocol non-stream), and dispatchPassthroughNonStream (same-
// protocol non-stream, both its Write and Flush failure branches).
func (s *Service) finalizeClientWrite(rc *Exchange, err error, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, statusCode int, start time.Time) attemptResult {
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, statusCode, AttemptConnError, "client write timeout: "+err.Error()))
	s.finalize(rc, 499, "client_write_timeout", start)
	return attemptSuccess
}

// finalizeClientWriteOrPartial handles a mid-stream failure that occurs after
// the first byte has already been sent to the client: the response is
// already committed, so the attempt can neither switch candidates nor change
// the HTTP status. It classifies err in priority order:
//
//  1. A downstream Write/Flush error (isClientWriteError — the dedicated
//     ErrClientWrite sentinel, wrapped at the exact write site that failed)
//     — a slow client, or the connection dying on THIS side while
//     forwarding. Checked FIRST because it is the most specific, unambiguous
//     signal available: it comes from the write call itself, not a
//     side-effect. This also has to run before case 2 below because a write
//     deadline expiring can itself cause c.Request.Context() to observe
//     Canceled shortly after (net/http's CloseNotify-activated background
//     reader — started by WatchClientClose — treats any error on the
//     connection, including one caused by the write side going bad, as a
//     signal to cancel the request context) — checking the disconnect
//     condition first would then misreport a genuine slow-client write
//     timeout as "client disconnected" depending on scheduling.
//  2. The caller's OWN connection was canceled (isClientDisconnected, or err
//     itself is context.Canceled) and case 1 didn't already match — a
//     genuine caller disconnect, not an upstream fault. No inline error
//     frame is attempted (the connection is dead) and this settles as 499
//     client_disconnected. This case only arises on the IR cross-protocol
//     path: passthrough's own stream pumps already turn a caller disconnect
//     into errClientDisconnected/499 before ever reaching here (see
//     dispatchPassthroughStream), and their pre-write ctx.Done() checks mean
//     a mid-stream error from THIS caller's own disconnect never surfaces as
//     anything else. Without this check, the IR path's context.Canceled
//     (surfaced as a plain "upstream stream read error: context canceled"
//     from the scanner, not wrapped in ErrClientWrite or
//     errClientDisconnected) fell through to the generic server-failure
//     branch below: an inline error frame was written to an already-dead
//     connection and the row was misrecorded as stream_partial (blaming the
//     provider for the caller's own hangup).
//  3. Any other error is a genuine mid-stream server failure, recorded after
//     emitting one inline SSE error event.
//
// Shared by processDispatchResponseStream (IR path) and
// dispatchPassthroughStream (passthrough path) — with case 2 now covering
// the caller-disconnect gap that used to make the two paths diverge, their
// mid-stream handling really is otherwise identical.
func (s *Service) finalizeClientWriteOrPartial(c *gin.Context, rc *Exchange, err error, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
	if isClientWriteError(err) {
		return s.finalizeClientWrite(rc, err, cand, provider, pk, resp.StatusCode, start)
	}
	if isClientDisconnected(c) || errors.Is(err, context.Canceled) {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptConnError, "client disconnected"))
		s.finalize(rc, 499, "client_disconnected", start)
		return attemptSuccess
	}
	_ = writeStreamErrorEvent(c, rc)
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream mid: "+err.Error()))
	s.finalize(rc, http.StatusOK, "stream_partial: "+err.Error(), start)
	return attemptSuccess
}

// processDispatchResponseNonStream routes a 2xx non-stream upstream response
// to the client for one attempt. It is attemptOne's (relay.go) non-stream
// counterpart to processDispatchResponseStream, called only after
// SendUpstreamRequest returned a 2xx status.
//
// Passthrough (egress.Passthrough — the only live path today, because
// every provider's provider_type is openai so Negotiate always echoes the
// ingress protocol; negotiate.go already implements cross-protocol egress, but
// no non-openai provider is created yet) calls dispatchPassthroughNonStream:
// byte-for-byte the same bounded-read + model-rewrite + usage-extract
// behavior the pre-IR gateway had, which the existing regression suite pins.
//
// Cross-protocol responses go through protocols.IRNonStreamRelay: decode via
// the egress protocol's ResponseDecoder, re-encode via the ingress
// protocol's ResponseEncoder. IRNonStreamRelay writes the client body (and
// records the raw upstream body into rc via SetBody) itself; this function
// only translates its outcome into an attemptResult.
func (s *Service) processDispatchResponseNonStream(
	c *gin.Context,
	rc *Exchange,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
	cand model.ModelCandidate,
	provider *model.Provider,
	pk model.ProviderKey,
	resp *http.Response,
	start time.Time,
) attemptResult {
	if egress.Passthrough {
		return s.dispatchPassthroughNonStream(c, rc, egress.Protocol, cand, provider, pk, resp, start)
	}

	decoder := codecsFor(egress.Protocol).ResponseDecoder
	// Force the caller-facing model field back to the external name,
	// exactly like the passthrough path's RewriteNonStreamResponse — the
	// decoded IR carries whatever model name the *upstream* echoed back
	// (the candidate's provider_model_name), which must never leak to the
	// caller.
	encoder := modelOverrideResponseEncoder{inner: codecsFor(ingress).ResponseEncoder, model: rc.originalModel}
	usage, err := protocols.IRNonStreamRelay(c, resp, decoder, encoder, rc, nil)
	if err != nil {
		if isClientWriteError(err) {
			// The response was already fully decoded and (partially) written
			// to the client — status + headers are committed, so this
			// candidate cannot fail over. Bytes never (fully) reached the
			// caller, so this must NOT settle as a delivered 2xx success:
			// record it as a client write failure (499), mirroring the
			// streaming mid-write handling (finalizeClientWriteOrPartial).
			// Usage is still preserved for billing — IRNonStreamRelay
			// returns it even on a write/flush failure, since the upstream
			// itself was already fully consumed regardless of delivery
			// outcome.
			rc.usage = irUsageToUsage(usage)
			return s.finalizeClientWrite(rc, err, cand, provider, pk, resp.StatusCode, start)
		}
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "ir decode: "+err.Error()))
		// IRNonStreamRelay's documented contract is to write nothing to the
		// client when the 2xx upstream body fails IR decode, so — like the
		// buildDispatchRequest error above — this candidate can still fail
		// over to the next one without corrupting an in-flight response.
		// The written branch below only guards against that contract
		// changing under us: once bytes are on the wire they cannot be
		// un-sent or handed to a different candidate, so the only safe move
		// left is to settle here.
		if c.Writer.Written() {
			s.finalize(rc, resp.StatusCode, "ir_decode_partial: "+err.Error(), start)
			return attemptSuccess
		}
		return attemptNextCandidate
	}

	rc.usage = irUsageToUsage(usage)
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
	s.finalize(rc, resp.StatusCode, "", start)
	return attemptSuccess
}

// ─────────────────── Same-protocol byte passthrough ───────────────────────
//
// The two functions below forward a same-protocol (ingress == egress) 2xx
// upstream response to the client byte-for-byte instead of round-tripping it
// through the IR: read (or scan) the raw upstream response, extract usage
// from the same bytes being forwarded, and write those bytes straight to the
// client. This avoids the IR codec's field allowlist silently dropping any
// vendor-specific field the IR doesn't model.
//
// Both layer in three behaviors a plain byte-for-byte passthrough would
// otherwise lack: the response model field is rewritten back to
// rc.originalModel (upstream only ever sees rc.candidate.ProviderModelName,
// which must never reach the caller); the usage field the gateway injects
// upstream purely for its own cost accounting is stripped from what the
// caller receives unless the caller actually asked for it
// (rc.wantsStreamUsage); and the caller-facing (post-rewrite/strip) bytes,
// not the raw upstream bytes, are what land in the request/stream body
// capture used for audit and debugging.

// passthroughRewriteNonStreamResponse rewrites a non-stream same-protocol
// upstream response's model field back to the external name and extracts
// usage, branching by egress protocol so the live OpenAI path is untouched.
//
// For an OpenAI egress this is exactly RewriteNonStreamResponse (unchanged
// production behavior). For a Claude or Responses egress, the model-field
// rewrite is rewriteModelField (it operates on any JSON body with a
// top-level "model" field, which both response shapes have). For a Gemini
// egress, the provider's model name is NOT in a top-level "model" field at
// all -- a Gemini generateContent response instead echoes it back in
// "modelVersion" (see internal/protocols/gemini/response.go's
// ResponseDecoder) -- so rewriteGeminiResponseModelVersion is used instead,
// which rewrites that field in place and, critically, does NOT add a
// top-level "model" key the real Gemini response shape never has.
//
// Usage in every non-OpenAI branch is extracted via that protocol's own
// ResponseDecoder instead of the OpenAI-shaped extractUsage -- Claude
// reports input_tokens/output_tokens under a "usage" object and Gemini
// reports promptTokenCount/candidatesTokenCount under "usageMetadata",
// neither of which extractUsage's prompt_tokens/completion_tokens field
// names ever match, so without this branch a non-OpenAI passthrough
// response was byte-forwarded correctly but silently left rc.usage nil (no
// billing).
//
// A decode error here does NOT fail the request: the caller-facing bytes are
// already correctly rewritten and about to be written to the client
// regardless, so usage is simply left nil (unknown, not zero) rather than
// failing this candidate over -- the response itself is not malformed, only
// unparseable for the gateway's own cost accounting.
func passthroughRewriteNonStreamResponse(egressProtocol protocols.ProtocolID, body []byte, externalModel string) ([]byte, *Usage, error) {
	if egressProtocol == protocols.ProtocolOpenAI {
		return RewriteNonStreamResponse(body, externalModel)
	}
	var rewritten []byte
	var err error
	if egressProtocol == protocols.ProtocolGemini {
		rewritten, err = rewriteGeminiResponseModelVersion(body, externalModel)
	} else {
		rewritten, err = rewriteModelField(body, externalModel)
	}
	if err != nil {
		return nil, nil, err
	}
	var usage *Usage
	if irResp, decErr := codecsFor(egressProtocol).ResponseDecoder.DecodeResponse(json.RawMessage(body)); decErr == nil && irResp != nil {
		usage = irUsageToUsage(&irResp.Usage)
	}
	return rewritten, usage, nil
}

// rewriteGeminiResponseModelVersion parses body as a JSON object and, if it
// carries a top-level "modelVersion" field (the field a Gemini
// generateContent response echoes the serving model name in), rewrites it to
// externalModel -- the Gemini counterpart of rewriteModelField's "model"
// rewrite. Unlike rewriteModelField, this deliberately does NOT add the
// field when it is absent: a real Gemini response body has no top-level
// "model" field at all, so a body that (unexpectedly) omits "modelVersion"
// is forwarded unchanged rather than gaining a field the wire shape never
// has.
func rewriteGeminiResponseModelVersion(body []byte, externalModel string) ([]byte, error) {
	return rewriteJSONStringField(body, "modelVersion", externalModel, true)
}

// dispatchPassthroughNonStream writes a 2xx non-stream same-protocol upstream
// response to the client: bounded read, model-field rewrite + usage
// extraction, write. Called only from processDispatchResponseNonStream's
// passthrough branch.
//
// egressProtocol is always equal to the ingress protocol here (that is what
// Passthrough means, see EgressDecision), and selects how the response is
// rewritten: an OpenAI egress keeps the original RewriteNonStreamResponse
// codepath verbatim (the only production-live passthrough path, see
// processDispatchResponseNonStream's doc comment); any other egress protocol
// (e.g. an Anthropic ingress passed through to an Anthropic provider) goes
// through passthroughRewriteNonStreamResponse instead, which reuses the same
// model-field rewrite but extracts usage via that protocol's own
// ResponseDecoder rather than assuming OpenAI's prompt_tokens/
// completion_tokens shape -- without this, a non-OpenAI passthrough response
// was byte-forwarded correctly but silently never billed (extractUsage always
// returned nil against a Claude usage object).
func (s *Service) dispatchPassthroughNonStream(c *gin.Context, rc *Exchange, egressProtocol protocols.ProtocolID, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
	defer func() { _ = resp.Body.Close() }()
	// Now committed to this 2xx candidate: drop any error body a prior failed
	// candidate stashed in these fields. The three failover returns below
	// (read error / oversize / rewrite error) don't refresh it, so without
	// this clear a stale earlier-candidate error body would be persisted as
	// THIS request's upstream response. Only the success path re-sets both
	// fields.
	rc.clearResponseBodies()
	// Bound the response read: a buggy or hostile upstream can otherwise
	// return an unbounded body and exhaust gateway memory before the
	// request timeout fires (the response body has no bodylimit guard the
	// way the request body does). Mirrors provider_client.go: read up to
	// N+1 bytes so an overflow is detectable, then failover.
	limited := io.LimitReader(resp.Body, maxNonStreamResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		// A caller disconnect during the body read is terminal (the caller
		// is gone), not a candidate failure — recognize it to avoid a
		// wasted failover attempt and to log 499, not bad_status.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptConnError, "client disconnected"))
			s.finalize(rc, 499, "client_disconnected", start)
			return attemptTerminal
		}
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "read body: "+err.Error()))
		return attemptNextCandidate
	}
	if int64(len(body)) > maxNonStreamResponseBytes {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "response too large"))
		return attemptNextCandidate
	}
	rewritten, usage, err := passthroughRewriteNonStreamResponse(egressProtocol, body, rc.originalModel)
	if err != nil {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "rewrite: "+err.Error()))
		return attemptNextCandidate
	}
	// Raw upstream (pre-rewrite, provider model name) is recorded regardless
	// of whether the write to the client below succeeds — it's what the
	// gateway actually consumed from upstream, useful for debugging even
	// when delivery fails. rc.responseBody (the caller-facing body) is only
	// set once the write below actually succeeds — see the write-failure
	// branch's comment for why.
	rc.upstreamResponseBody = body
	rc.usage = usage
	c.Header("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	if _, werr := c.Writer.Write(rewritten); werr != nil {
		// Status + headers are already committed, so this candidate cannot
		// fail over. The client never (fully) received these bytes — leave
		// rc.responseBody unset (cleared by rc.clearResponseBodies() above)
		// so the audit never records an undelivered response as "sent", and
		// settle this as a client write failure (499), not a delivered 2xx
		// success — mirrors the streaming mid-write handling
		// (finalizeClientWriteOrPartial). Usage is still preserved above for
		// billing: the upstream was already fully consumed regardless of
		// delivery outcome.
		return s.finalizeClientWrite(rc, werr, cand, provider, pk, resp.StatusCode, start)
	}
	// A few KB of non-stream JSON can land entirely inside net/http's
	// internal buffer: Write returns nil without the bytes actually
	// reaching the socket, and the real delivery error only surfaces on
	// Flush. Without this check a client that disconnects right after a
	// buffered Write is still recorded as a delivered 2xx — the same class
	// of bug the streaming write path already guards against.
	if ferr := flushAndCheckError(c); ferr != nil {
		return s.finalizeClientWrite(rc, ferr, cand, provider, pk, resp.StatusCode, start)
	}
	rc.responseBody = rewritten
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
	s.finalize(rc, resp.StatusCode, "", start)
	return attemptSuccess
}

// dispatchPassthroughStream drives the stream pump (passthroughStreamToClient
// for an OpenAI egress, passthroughStreamToClientDecoded for any other) and
// maps its outcome to an attemptResult, mirroring
// dispatchPassthroughNonStream's role for the stream case. Called only from
// processDispatchResponseStream's passthrough branch.
//
// egressProtocol is always equal to the ingress protocol here (Passthrough's
// definition, see EgressDecision). The OpenAI branch is byte-for-byte the
// original pump, unmodified (the only production-live passthrough path). Any
// other egress protocol (e.g. an Anthropic ingress passed through to an
// Anthropic provider) goes through passthroughStreamToClientDecoded instead:
// same byte-forwarding, but usage/completion detection go through that
// protocol's own StreamDecoder rather than the OpenAI-specific
// prompt_tokens/completion_tokens usage shape and `data: [DONE]` terminator
// — without this, a non-OpenAI passthrough stream was byte-forwarded
// correctly but never billed (rc.usage stayed nil) and always finalized as
// "stream_no_done" (the [DONE] literal never appears on a Claude stream)
// even when the upstream completed cleanly with message_stop.
func (s *Service) dispatchPassthroughStream(c *gin.Context, rc *Exchange, egressProtocol protocols.ProtocolID, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
	// A prior failed candidate may have stashed its non-2xx error body in
	// these fields. A stream request persists its response through the SSE
	// capture file, not these fields — the types.go contract is that both
	// stay empty for stream requests. Now that we've committed to streaming
	// a 2xx candidate, drop any stale error body so the detail page doesn't
	// show a previous candidate's error as this (successful) request's
	// "upstream response".
	rc.clearResponseBodies()
	var usage *Usage
	var err error
	if egressProtocol == protocols.ProtocolOpenAI {
		usage, err = passthroughStreamToClient(c, resp, rc)
	} else {
		usage, err = passthroughStreamToClientDecoded(c, resp, rc, egressProtocol)
	}
	// Both pumps deliberately leave the capture file open past their own
	// return, so the writeStreamErrorEvent call below can still append to
	// it; this function is the single place responsible for closing it, on
	// every exit path.
	defer closeStreamBodyFile(rc)
	if usage != nil {
		rc.usage = usage // preserve partial usage even on error paths
	}
	// If the stream never produced a sent byte, the capture file (if any)
	// is empty — remove it so the detail page never shows an empty "stream
	// body" link (covers both the pre-first-byte failover path below and an
	// early client-disconnect).
	removeEmptyStreamBodyFile(c, rc)
	if err == nil {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
		s.finalize(rc, http.StatusOK, "", start)
		return attemptSuccess
	}
	if errors.Is(err, errClientDisconnected) {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptConnError, "client disconnected"))
		s.finalize(rc, 499, "client_disconnected", start)
		return attemptSuccess // already streamed partial content, terminal
	}
	if errors.Is(err, errStreamNoDoneTerminator) {
		// Upstream sent content but closed without the [DONE] terminator.
		// Do NOT inject a synthetic error event — the caller already
		// received the content bytes, and most OpenAI SDKs handle a plain
		// EOF gracefully. Injecting an error event would break a possibly-
		// complete completion (some upstreams stably omit [DONE]). Only
		// mark the log row partial so the missing terminator is traceable.
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream ended without [DONE]"))
		s.finalize(rc, http.StatusOK, "stream_no_done", start)
		return attemptSuccess
	}
	if rc.firstByteSent {
		// Mid-stream failure after the first byte — can't switch, can't
		// change HTTP status; same handling as
		// processDispatchResponseStream's mid-stream branch.
		return s.finalizeClientWriteOrPartial(c, rc, err, cand, provider, pk, resp, start)
	}
	// Pre-first-byte failure: nothing written yet, can still failover.
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream start: "+err.Error()))
	return attemptNextCandidate
}

// passthroughStreamToClient pipes an SSE stream from upstream to the client,
// rewriting the model field in every `data: {json}` chunk back to the
// external name. Returns the usage from the final usage chunk
// if the upstream sent one, and an error only for transport-level failures.
//
// Header is deferred until the first data frame: if the upstream returns 2xx
// but EOFs (or errors) before emitting any data, nothing has been written to
// the client yet and the relay loop can still failover to the next candidate
// (lifecycle table — "received upstream response, first chunk not
// yet sent to caller → failover allowed"). Once a data frame is forwarded,
// rc.firstByteSent flips true and no more switching is allowed.
//
// Leading non-data lines before the first data frame (commentary / SSE
// preamble) are skipped — OpenAI chat streams open with `data:` directly, so
// this only matters for unusual upstreams, and skipping keeps the failover
// window intact.
//
// The [DONE] terminator is tracked: an upstream that sends data frames and
// then closes cleanly WITHOUT `data: [DONE]` has truncated the completion —
// the pump returns errStreamNoDoneTerminator so dispatchPassthroughStream
// records a partial row instead of clean success.
//
// When the caller did NOT request stream_options.include_usage but the
// gateway injected it upstream (EnsureStreamUsageInjection), the usage field
// is stripped from forwarded frames (injected usage is for the
// gateway's internal cost accounting only).
func passthroughStreamToClient(c *gin.Context, resp *http.Response, rc *Exchange) (*Usage, error) {
	defer func() { _ = resp.Body.Close() }()

	// Append every SENT SSE line (post-rewrite, post-usage-strip =
	// caller-facing) to a per-request file under data/bodies/. Memory stays
	// bounded (one line at a time); the full stream is persisted without
	// truncation. The 1GiB backstop only sets a truncation flag (never
	// silent) for a hostile upstream.
	//
	// The file is opened here but deliberately NOT closed before this
	// function returns: a mid-stream failure after the first byte causes
	// dispatchPassthroughStream to call writeStreamErrorEvent, which writes
	// one more inline SSE error frame + a synthetic [DONE] straight to the
	// client — those bytes must also land in the capture file, so the file
	// has to stay open across that call. dispatchPassthroughStream closes it
	// via closeStreamBodyFile once it has finished writing everything it's
	// going to write for this attempt.
	openStreamBodyFile(c, rc)

	headerWritten := false
	doneSeen := false // tracks whether upstream emitted the `data: [DONE]` terminator
	var usage *Usage
	// preamble buffers any SSE lines that arrive before the first data
	// frame (commentary heartbeats, event:/id:/retry: directives, blank
	// separators). They're flushed in order once the first data frame
	// commits the headers — kept intact rather than dropped, so an upstream
	// that opens with a preamble doesn't lose framing, while the failover
	// window still only closes once actual data has reached the client.
	var preamble []byte
	// bufio.Scanner (not Reader.ReadBytes) bounds a single line's memory at
	// maxStreamLineBytes — ReadBytes allocates the whole line before any
	// length check could fire, so the cap was decorative. Scanner.Buffer's
	// second arg is the max token size; exceeding it surfaces as
	// bufio.ErrTooLong from Err().
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLineBytes)
	for scanner.Scan() {
		// Caller disconnect -> stop reading upstream and release
		// the concurrency slot. Checked before each forwarded line so a
		// disconnect between chunks is caught promptly.
		select {
		case <-c.Request.Context().Done():
			// Post-[DONE] disconnect -> success; pre-[DONE] -> 499. See
			// clientDisconnectOutcome for the full rationale.
			return clientDisconnectOutcome(usage, doneSeen)
		default:
		}
		// ScanLines strips the trailing newline; re-add it so writeStreamLine
		// sees the same shape ReadBytes would have produced.
		line := append(scanner.Bytes(), '\n')
		switch {
		case headerWritten:
			// Slide the per-write deadline before each forwarded line so
			// a slow-reading client is bounded by streamWriteWindow.
			applyStreamWriteDeadline(c, rc.requestID)
			// A downstream Write/Flush error here (deadline exceeded, broken
			// pipe) must be wrapped in protocols.ErrClientWrite and returned
			// immediately: this is the production streaming path (every
			// provider is openai today, so egress.Passthrough is always
			// true — see dispatchPassthroughStream's doc comment), so a
			// swallowed error here is what previously let a slow client hold
			// its concurrency slot past the sliding write-deadline
			// protection entirely. isClientWriteError (dispatch.go) only
			// recognizes the sentinel, not net.Error.Timeout(), because
			// context.DeadlineExceeded from the upstream attempt also
			// satisfies that interface.
			sent, werr := forwardStreamLine(c, rc, line, &usage, &doneSeen)
			if werr != nil {
				return usage, fmt.Errorf("%w: passthrough write to client: %w", protocols.ErrClientWrite, werr)
			}
			if ferr := flushAndCheckError(c); ferr != nil {
				return usage, fmt.Errorf("%w: passthrough flush to client: %w", protocols.ErrClientWrite, ferr)
			}
			appendStreamBodyLine(rc, sent)
		case isDataLine(line):
			// First data frame — commit the SSE headers, flush any buffered
			// preamble in order, then forward the data line.
			applyStreamWriteDeadline(c, rc.requestID)
			writeSSEHeader(c)
			// The 200 status + SSE headers are committed the moment
			// writeSSEHeader returns — mark first-byte-sent now, before the
			// preamble/data write below, so a write failure in either one is
			// classified as a mid-stream failure (no failover, inline error
			// frame skipped in favor of 499) rather than a pre-first-byte one:
			// the caller already has a 200 on the wire regardless of whether
			// these particular bytes land.
			rc.MarkFirstByteSent()
			hadPreamble := len(preamble) > 0
			if hadPreamble {
				if _, err := c.Writer.Write(preamble); err != nil {
					return usage, fmt.Errorf("%w: passthrough write preamble to client: %w", protocols.ErrClientWrite, err)
				}
			}
			headerWritten = true
			sent, werr := forwardStreamLine(c, rc, line, &usage, &doneSeen)
			if werr != nil {
				return usage, fmt.Errorf("%w: passthrough write to client: %w", protocols.ErrClientWrite, werr)
			}
			if ferr := flushAndCheckError(c); ferr != nil {
				return usage, fmt.Errorf("%w: passthrough flush to client: %w", protocols.ErrClientWrite, ferr)
			}
			if hadPreamble {
				// The preamble bytes were just sent to the caller — capture
				// them too, or the persisted stream body silently starts
				// after whatever heartbeat/event:/id:/retry: lines the
				// upstream opened with.
				appendStreamBodyLine(rc, preamble)
				preamble = nil
			}
			appendStreamBodyLine(rc, sent)
		default:
			// Preamble line before the first data frame — buffer it, but
			// cap the buffer so a malicious/buggy upstream can't grow it
			// without bound (the response body has no bodylimit guard the
			// way the request body does).
			if len(preamble)+len(line) <= maxPreambleBytes {
				preamble = append(preamble, line...)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// A caller disconnect surfaces as a body-read error (the transport
		// cancels on ctx.Done); recognize it so dispatchPassthroughStream logs
		// 499 instead of an upstream stream fault.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			// Same post-[DONE] disconnect as the in-loop select guard, but
			// surfaced here as a ctx-canceled body-read error instead.
			return clientDisconnectOutcome(usage, doneSeen)
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return usage, fmt.Errorf("upstream stream line too long (max %d bytes)", maxStreamLineBytes)
		}
		// Some upstreams close the connection non-standardly (HTTP/2 stream
		// reset, an unexpected-EOF translation of a chunked-body tail) right
		// after emitting the [DONE] terminator and a full usage frame. Once
		// both have already been observed, that trailing transport error is
		// not a genuine interruption — swallow it as success instead of
		// misreporting a fully-delivered stream as failed.
		if doneSeen && usage != nil && protocols.IsBenignPostDoneReadErr(err) {
			return usage, nil
		}
		return usage, fmt.Errorf("read upstream stream: %w", err)
	}
	// scanner.Scan() returned false with nil err = clean EOF.
	if !headerWritten {
		return usage, errors.New("upstream stream ended before any data chunk")
	}
	if !doneSeen {
		// Upstream emitted at least one data frame but closed without the
		// [DONE] terminator — the completion is silently truncated. Report
		// it so dispatchPassthroughStream logs a partial row instead of
		// clean success. Bytes already went to the client, so the HTTP
		// status stays 200.
		return usage, errStreamNoDoneTerminator
	}
	return usage, nil
}

// passthroughStreamToClientDecoded is passthroughStreamToClient's non-OpenAI
// counterpart: it forwards every upstream SSE line to the client
// byte-for-byte, exactly like passthroughStreamToClient, but deliberately
// does NOT attempt the OpenAI-specific per-chunk model-field rewrite
// (rewriteStreamChunk assumes an OpenAI chat.completion.chunk JSON shape,
// which a Claude/Responses/Gemini SSE event does not have — a Claude
// message_start event, for instance, nests "model" under "message", not at
// the top level).
//
// Every egress protocol handled here DOES get its own model rewrite, applied
// per-line via rewritePassthroughStreamModel: Claude nests the model once,
// in the single message_start frame (rewriteClaudeMessageStartModel, latched
// via a once-flag at the call site below since a Claude stream only ever
// sends one); Gemini repeats "modelVersion" at the top level of every single
// chunk (rewriteGeminiStreamModelVersion, applied to every forwarded line,
// no once-flag); Responses nests the model at "response.model", but only on
// the response.created/response.completed/response.in_progress envelope
// events (rewriteResponsesStreamModel). Every one of these leaves any line
// it doesn't recognize completely unchanged, so the rest of the stream is
// still forwarded byte-for-byte regardless of protocol.
//
// Every forwarded line is also fed into egressProtocol's own StreamDecoder
// purely to accumulate usage (DeltaUsage) and detect a clean completion
// (FinishSignals.SawDone — Claude: message_stop / a message_delta carrying
// stop_reason) — the same technique protocols.IRStreamRelay uses for the
// cross-protocol path. The decoded deltas themselves are discarded after
// accumulation (never re-encoded): the bytes already on the wire, forwarded
// verbatim above, are the caller-facing response.
//
// Returns the accumulated usage and an error only for transport-level
// failures; reuses errClientDisconnected / errStreamNoDoneTerminator (the
// SAME sentinel values passthroughStreamToClient returns) so
// dispatchPassthroughStream's existing error-branch handling (mid-stream vs.
// pre-first-byte failover, 499 vs. stream_no_done, ...) applies unchanged to
// both pumps without any caller-side branching beyond picking which pump to
// run.
// rewriteSSEDataLineJSON applies mutate to the JSON object carried by a
// "data: {...}" SSE line. Returns the rewritten line and true only if it was a
// data line whose payload parsed and mutate reported a change; otherwise
// returns the original line and false. Preserves the trailing newline bytes.
func rewriteSSEDataLineJSON(line []byte, mutate func(obj map[string]json.RawMessage) bool) ([]byte, bool) {
	const prefix = "data: "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return line, false
	}
	payload := line[len(prefix):]
	trimmed := bytes.TrimRight(payload, "\r\n")
	trailing := payload[len(trimmed):]

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return line, false
	}
	if !mutate(envelope) {
		return line, false
	}
	newEnvelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return line, false
	}
	newLine := make([]byte, 0, len(prefix)+len(newEnvelopeJSON)+len(trailing))
	newLine = append(newLine, prefix...)
	newLine = append(newLine, newEnvelopeJSON...)
	newLine = append(newLine, trailing...)
	return newLine, true
}

// rewriteClaudeMessageStartModel rewrites the model field nested in a Claude
// message_start SSE data line to the external model name, so a passthrough
// Claude stream never leaks the provider's internal model name. Any line that
// is not a "data: " line, not a message_start frame, or not parseable is
// returned unchanged (ok=false) so the rest of the stream is still forwarded
// byte-for-byte. Preserves the trailing newline(s).
func rewriteClaudeMessageStartModel(line []byte, externalModel string) ([]byte, bool) {
	// Fast pre-check to skip the JSON parse on every other frame — a Claude
	// stream sends exactly one message_start per response, so this substring
	// check is false for the (many) content_block_delta/message_delta lines.
	if !bytes.Contains(line, []byte("message_start")) {
		return line, false
	}
	return rewriteSSEDataLineJSON(line, func(envelope map[string]json.RawMessage) bool {
		var typ string
		if err := json.Unmarshal(envelope["type"], &typ); err != nil || typ != "message_start" {
			return false
		}
		msgRaw, ok := envelope["message"]
		if !ok {
			return false
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(msgRaw, &message); err != nil {
			return false
		}
		modelJSON, err := json.Marshal(externalModel)
		if err != nil {
			return false
		}
		message["model"] = modelJSON
		newMsgJSON, err := json.Marshal(message)
		if err != nil {
			return false
		}
		envelope["message"] = newMsgJSON
		return true
	})
}

// rewriteGeminiStreamModelVersion rewrites a Gemini stream chunk's top-level
// "modelVersion" field to the external model name. Unlike Claude's single
// message_start frame, every Gemini streamGenerateContent chunk carries
// "modelVersion", so this is applied to every forwarded line (no once-flag)
// — see the call site in passthroughStreamToClientDecoded. Any line that is
// not a "data: " line, has no "modelVersion" field (the cheap substring
// pre-check below skips the JSON parse for the common case), or fails to
// parse is returned unchanged (ok=false) so the rest of the stream is still
// forwarded byte-for-byte. Preserves the trailing newline(s).
func rewriteGeminiStreamModelVersion(line []byte, externalModel string) ([]byte, bool) {
	if !bytes.Contains(line, []byte("modelVersion")) {
		return line, false
	}
	return rewriteSSEDataLineJSON(line, func(envelope map[string]json.RawMessage) bool {
		if _, present := envelope["modelVersion"]; !present {
			return false
		}
		modelJSON, err := json.Marshal(externalModel)
		if err != nil {
			return false
		}
		envelope["modelVersion"] = modelJSON
		return true
	})
}

// rewriteResponsesStreamModel rewrites the nested "response.model" field
// carried by a Responses SSE envelope event (response.created,
// response.in_progress, response.completed — see
// internal/protocols/responses/encoder.go's ensureCreated/makeCompleted;
// every other Responses event type has no top-level "response" object at
// all) to the external model name. Applied to every forwarded line, gated
// only on whether that line's envelope actually nests a "model" field under
// "response" — not on a fixed event-type allowlist — so this also covers
// any other envelope event a real upstream might emit the model on. Any
// line that is not a "data: " line, has no "response"/"model" shape (the
// cheap substring pre-check below skips the JSON parse for the common
// per-token delta events), or fails to parse is returned unchanged
// (ok=false). Preserves the trailing newline(s).
func rewriteResponsesStreamModel(line []byte, externalModel string) ([]byte, bool) {
	if !bytes.Contains(line, []byte(`"response"`)) || !bytes.Contains(line, []byte(`"model"`)) {
		return line, false
	}
	return rewriteSSEDataLineJSON(line, func(envelope map[string]json.RawMessage) bool {
		respRaw, ok := envelope["response"]
		if !ok {
			return false
		}
		var respObj map[string]json.RawMessage
		if err := json.Unmarshal(respRaw, &respObj); err != nil {
			return false
		}
		if _, present := respObj["model"]; !present {
			return false
		}
		modelJSON, err := json.Marshal(externalModel)
		if err != nil {
			return false
		}
		respObj["model"] = modelJSON
		newRespJSON, err := json.Marshal(respObj)
		if err != nil {
			return false
		}
		envelope["response"] = newRespJSON
		return true
	})
}

// rewritePassthroughStreamModel rewrites the model name embedded in one
// upstream SSE line back to the external caller-facing name, dispatching by
// egress protocol on where (and how often) that protocol's stream shape
// carries it: Claude nests it once, in the message_start frame
// (claudeModelRewritten latches true the first time that frame is
// successfully rewritten, so later lines — none of which carry the model in
// practice — skip the attempt); Gemini repeats "modelVersion" at the top
// level of every chunk, so it is checked (and, if present, rewritten) on
// every line; Responses nests it under "response.model" on the envelope
// events that carry a "response" object. Any egress protocol not listed
// here (OpenAI, which never reaches this pump — see
// dispatchPassthroughStream) or any line the relevant helper doesn't
// recognize is returned completely unchanged.
func rewritePassthroughStreamModel(egressProtocol protocols.ProtocolID, line []byte, externalModel string, claudeModelRewritten *bool) []byte {
	switch egressProtocol {
	case protocols.ProtocolClaude:
		if !*claudeModelRewritten {
			if newLine, ok := rewriteClaudeMessageStartModel(line, externalModel); ok {
				*claudeModelRewritten = true
				return newLine
			}
		}
	case protocols.ProtocolGemini:
		if newLine, ok := rewriteGeminiStreamModelVersion(line, externalModel); ok {
			return newLine
		}
	case protocols.ProtocolResponses:
		if newLine, ok := rewriteResponsesStreamModel(line, externalModel); ok {
			return newLine
		}
	}
	return line
}

func passthroughStreamToClientDecoded(c *gin.Context, resp *http.Response, rc *Exchange, egressProtocol protocols.ProtocolID) (*Usage, error) {
	defer func() { _ = resp.Body.Close() }()

	// Same capture-file lifecycle as passthroughStreamToClient: opened here,
	// deliberately left open past this function's return so
	// dispatchPassthroughStream's writeStreamErrorEvent can still append a
	// mid-stream error frame to it; closed by dispatchPassthroughStream's
	// defer once every write for this attempt is done.
	openStreamBodyFile(c, rc)

	decoder := codecsFor(egressProtocol).NewStreamDecoder()
	var sig protocols.FinishSignals
	var accUsage protocols.IRUsage
	accumulate := func(deltas []protocols.IRStreamDelta) {
		sig.Accumulate(deltas)
		for _, d := range deltas {
			if du, ok := d.(protocols.DeltaUsage); ok {
				accUsage.Merge(du.Usage)
			}
		}
	}
	// irUsageToUsage nils out an all-zero usage (unknown, not zero) —
	// evaluated fresh on every return path so a mid-stream failure still
	// preserves whatever usage was collected before the failure, mirroring
	// passthroughStreamToClient's *usage return.
	currentUsage := func() *Usage { return irUsageToUsage(&accUsage) }

	headerWritten := false
	var preamble []byte
	// claudeModelRewritten latches true once the (single, per-stream)
	// message_start frame has been rewritten, so later lines that also
	// happen to contain the substring "message_start" (there aren't any in
	// practice, but the pre-check is substring-based) skip the parse attempt
	// too. Only meaningful for a Claude egress; unused (stays false) for
	// every other protocol, which rewritePassthroughStreamModel re-checks on
	// every line instead.
	claudeModelRewritten := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLineBytes)
	for scanner.Scan() {
		select {
		case <-c.Request.Context().Done():
			return clientDisconnectOutcome(currentUsage(), sig.SawDone)
		default:
		}
		line := append(scanner.Bytes(), '\n')
		// Rewrite the model name embedded in this line back to the external
		// caller-facing name, before the decoder feed and the writes below,
		// so the client, the usage decoder, and the stream capture file all
		// see the rewritten bytes. See rewritePassthroughStreamModel's doc
		// comment for how each egress protocol's stream shape carries it.
		line = rewritePassthroughStreamModel(egressProtocol, line, rc.originalModel, &claudeModelRewritten)
		deltas, _ := decoder.DecodeChunk(string(line))
		accumulate(deltas)

		switch {
		case headerWritten:
			// Slide the per-write deadline so a slow-reading client is
			// bounded by streamWriteWindow. A Write/Flush error is wrapped in
			// protocols.ErrClientWrite and returned immediately — see
			// passthroughStreamToClient's identical headerWritten branch for
			// the full rationale (this is the production streaming path for
			// every non-OpenAI passthrough egress).
			applyStreamWriteDeadline(c, rc.requestID)
			if _, err := c.Writer.Write(line); err != nil {
				return currentUsage(), fmt.Errorf("%w: passthrough write to client: %w", protocols.ErrClientWrite, err)
			}
			if ferr := flushAndCheckError(c); ferr != nil {
				return currentUsage(), fmt.Errorf("%w: passthrough flush to client: %w", protocols.ErrClientWrite, ferr)
			}
			appendStreamBodyLine(rc, line)
		case isDataLine(line):
			// First data frame — commit the SSE headers, flush any buffered
			// preamble in order, then forward the data line. Mirrors
			// passthroughStreamToClient's identical first-data-frame handling.
			applyStreamWriteDeadline(c, rc.requestID)
			writeSSEHeader(c)
			// The 200 status + SSE headers are committed the moment
			// writeSSEHeader returns — mark first-byte-sent now, before the
			// preamble/data write below, so a write failure in either one is
			// classified as mid-stream (no failover) rather than
			// pre-first-byte, mirroring passthroughStreamToClient.
			rc.MarkFirstByteSent()
			hadPreamble := len(preamble) > 0
			if hadPreamble {
				if _, err := c.Writer.Write(preamble); err != nil {
					return currentUsage(), fmt.Errorf("%w: passthrough write preamble to client: %w", protocols.ErrClientWrite, err)
				}
			}
			headerWritten = true
			if _, err := c.Writer.Write(line); err != nil {
				return currentUsage(), fmt.Errorf("%w: passthrough write to client: %w", protocols.ErrClientWrite, err)
			}
			if ferr := flushAndCheckError(c); ferr != nil {
				return currentUsage(), fmt.Errorf("%w: passthrough flush to client: %w", protocols.ErrClientWrite, ferr)
			}
			if hadPreamble {
				appendStreamBodyLine(rc, preamble)
				preamble = nil
			}
			appendStreamBodyLine(rc, line)
		default:
			// Preamble line before the first data frame — buffer it, capped
			// exactly like passthroughStreamToClient's preamble handling.
			if len(preamble)+len(line) <= maxPreambleBytes {
				preamble = append(preamble, line...)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			return clientDisconnectOutcome(currentUsage(), sig.SawDone)
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return currentUsage(), fmt.Errorf("upstream stream line too long (max %d bytes)", maxStreamLineBytes)
		}
		if sig.SawDone && protocols.IsBenignPostDoneReadErr(err) {
			return currentUsage(), nil
		}
		return currentUsage(), fmt.Errorf("read upstream stream: %w", err)
	}
	// Clean EOF: flush the decoder's own internal buffer before judging
	// sig.SawDone, mirroring protocols.IRStreamRelay's identical use of
	// decoder.Finish() — some decoders only emit their terminal delta once a
	// trailing boundary confirms the buffered event is complete, which a
	// clean EOF right after the last forwarded byte would otherwise miss.
	if deltas, _ := decoder.Finish(); len(deltas) > 0 {
		accumulate(deltas)
	}
	if !headerWritten {
		return currentUsage(), errors.New("upstream stream ended before any data chunk")
	}
	if !sig.SawDone {
		// Upstream emitted at least one data frame but the decoder never saw
		// a completion signal (Claude: no message_stop / no message_delta
		// stop_reason) — the completion is silently truncated. Report it, so
		// dispatchPassthroughStream logs a partial row instead of clean
		// success, exactly like passthroughStreamToClient's [DONE] check —
		// but keyed on the protocol's own completion signal instead of the
		// OpenAI-only [DONE] literal.
		return currentUsage(), errStreamNoDoneTerminator
	}
	return currentUsage(), nil
}
