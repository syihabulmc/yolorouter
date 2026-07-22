package gateway

import (
	"bufio"
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
func (s *RelayService) buildUpstreamBody(
	rc *RelayContext,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
) ([]byte, string, error) {
	if egress == nil {
		return nil, "", fmt.Errorf("gateway: buildUpstreamBody requires a non-nil egress decision")
	}

	providerModelName := rc.Candidate.ProviderModelName
	egressCodecs := codecsFor(egress.Protocol)

	var outBody []byte
	if egress.Passthrough {
		rewritten, err := rewriteModelField(rc.RequestBody, providerModelName)
		if err != nil {
			return nil, "", fmt.Errorf("rewrite model field: %w", err)
		}
		outBody = rewritten
	} else {
		dec := codecsFor(ingress).RequestDecoder
		ir, err := dec.DecodeRequest(json.RawMessage(rc.RequestBody), providerModelName, rc.IsStream)
		if err != nil {
			return nil, "", fmt.Errorf("decode ingress request (%s): %w", ingress, err)
		}
		encoded, err := egressCodecs.RequestEncoder.EncodeRequest(ir)
		if err != nil {
			return nil, "", fmt.Errorf("encode egress request (%s): %w", egress.Protocol, err)
		}
		outBody = []byte(encoded)
	}

	if egress.Protocol == protocols.ProtocolOpenAI {
		injected, err := EnsureStreamUsageInjection(outBody, rc.IsStream, rc.WantsStreamUsage)
		if err != nil {
			return nil, "", fmt.Errorf("inject stream usage: %w", err)
		}
		outBody = injected
	}

	path := egressCodecs.RequestEncoder.EgressPath(providerModelName, rc.IsStream)
	url := protocols.JoinUpstreamURL(egress.BaseURL, path, egress.Protocol)
	if egress.Protocol == protocols.ProtocolGemini && rc.IsStream {
		url = strings.Replace(url, ":generateContent", ":streamGenerateContent?alt=sse", 1)
	}

	return outBody, url, nil
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
// Known gap (tracked separately, deliberately not addressed here): unlike
// the passthrough branch, this path does not populate the caller-facing
// stream capture file (streamBodyFile) — protocols.IRStreamRelay records the
// raw upstream lines via rc.AppendUpstream, not the caller-facing
// (post-re-encode) bytes actually written to the client.
func (s *RelayService) processDispatchResponseStream(
	c *gin.Context,
	rc *RelayContext,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
	cand model.ModelCandidate,
	provider *model.Provider,
	pk model.ProviderKey,
	resp *http.Response,
	start time.Time,
) attemptResult {
	if egress.Passthrough {
		return s.dispatchPassthroughStream(c, rc, cand, provider, pk, resp, start)
	}

	// Now committed to this 2xx candidate — a stream request never
	// populates these two fields itself (its response lives in the stream
	// capture file instead), but a prior failed candidate may have stashed
	// a stale error body in them.
	rc.clearResponseBodies()

	egressDecoder := codecsFor(egress.Protocol).NewStreamDecoder()
	innerStreamEncoder := codecsFor(ingress).NewStreamEncoder()
	// Only an OpenAI ingress has a stream_options.include_usage knob; wire
	// it onto the concrete chat encoder before it gets wrapped below, since
	// modelOverrideStreamEncoder only exposes the protocols.StreamEncoder
	// interface (no IncludeUsage field) to the rest of this function.
	if ingress == protocols.ProtocolOpenAI {
		if chatEncoder, ok := innerStreamEncoder.(*chat.StreamEncoder); ok {
			chatEncoder.IncludeUsage = rc.WantsStreamUsage
		}
	}
	ingressEncoder := modelOverrideStreamEncoder{
		inner: innerStreamEncoder,
		model: rc.OriginalModel,
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
	rc.Usage = irUsageToUsage(usage)

	if err == nil {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
		s.finalize(rc, http.StatusOK, "", start)
		return attemptSuccess
	}
	if rc.FirstByteSent {
		// Mid-stream failure after the response headers/first bytes went out
		// — can't switch, can't change HTTP status; emit one inline SSE error
		// event + close, same as dispatchPassthroughStream's mid-stream
		// branch.
		writeStreamErrorEvent(c, rc)
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream mid: "+err.Error()))
		s.finalize(rc, http.StatusOK, "stream_partial: "+err.Error(), start)
		return attemptSuccess
	}
	// Pre-first-byte failure: the IR relay returned before ever writing
	// headers to the client (onFirstChunk/rc.MarkFirstByteSent never
	// fired), so nothing has been committed yet and this candidate can
	// still fail over to the next one — mirrors
	// dispatchPassthroughStream's pre-first-byte failover branch.
	rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream start: "+err.Error()))
	return attemptNextCandidate
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
func (s *RelayService) processDispatchResponseNonStream(
	c *gin.Context,
	rc *RelayContext,
	ingress protocols.ProtocolID,
	egress *EgressDecision,
	cand model.ModelCandidate,
	provider *model.Provider,
	pk model.ProviderKey,
	resp *http.Response,
	start time.Time,
) attemptResult {
	if egress.Passthrough {
		return s.dispatchPassthroughNonStream(c, rc, cand, provider, pk, resp, start)
	}

	decoder := codecsFor(egress.Protocol).ResponseDecoder
	// Force the caller-facing model field back to the external name,
	// exactly like the passthrough path's RewriteNonStreamResponse — the
	// decoded IR carries whatever model name the *upstream* echoed back
	// (the candidate's provider_model_name), which must never leak to the
	// caller.
	encoder := modelOverrideResponseEncoder{inner: codecsFor(ingress).ResponseEncoder, model: rc.OriginalModel}
	usage, err := protocols.IRNonStreamRelay(c, resp, decoder, encoder, rc, nil)
	if err != nil {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "ir decode: "+err.Error()))
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

	rc.Usage = irUsageToUsage(usage)
	rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
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
// rc.OriginalModel (upstream only ever sees rc.Candidate.ProviderModelName,
// which must never reach the caller); the usage field the gateway injects
// upstream purely for its own cost accounting is stripped from what the
// caller receives unless the caller actually asked for it
// (rc.WantsStreamUsage); and the caller-facing (post-rewrite/strip) bytes,
// not the raw upstream bytes, are what land in the request/stream body
// capture used for audit and debugging.

// dispatchPassthroughNonStream writes a 2xx non-stream same-protocol upstream
// response to the client: bounded read, model-field rewrite + usage
// extraction via RewriteNonStreamResponse, write. Called only from
// processDispatchResponseNonStream's passthrough branch.
func (s *RelayService) dispatchPassthroughNonStream(c *gin.Context, rc *RelayContext, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
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
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptConnError, "client disconnected"))
			s.finalize(rc, 499, "client_disconnected", start)
			return attemptTerminal
		}
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "read body: "+err.Error()))
		return attemptNextCandidate
	}
	if int64(len(body)) > maxNonStreamResponseBytes {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "response too large"))
		return attemptNextCandidate
	}
	rewritten, usage, err := RewriteNonStreamResponse(body, rc.OriginalModel)
	if err != nil {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptBadStatus, "rewrite: "+err.Error()))
		return attemptNextCandidate
	}
	// Raw upstream (pre-rewrite, provider model name) vs.
	// caller-facing (post-rewrite, external model name) — these two differ
	// only in the model field, but both must be recorded. Stored
	// verbatim (v0.1 does not scrub body content).
	rc.UpstreamResponseBody = body
	rc.ResponseBody = rewritten
	rc.Usage = usage
	rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
	c.Header("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(rewritten)
	s.finalize(rc, resp.StatusCode, "", start)
	return attemptSuccess
}

// dispatchPassthroughStream drives the stream pump (passthroughStreamToClient)
// and maps its outcome to an attemptResult, mirroring
// dispatchPassthroughNonStream's role for the stream case. Called only from
// processDispatchResponseStream's passthrough branch.
func (s *RelayService) dispatchPassthroughStream(c *gin.Context, rc *RelayContext, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
	// A prior failed candidate may have stashed its non-2xx error body in
	// these fields. A stream request persists its response through the SSE
	// capture file, not these fields — the types.go contract is that both
	// stay empty for stream requests. Now that we've committed to streaming
	// a 2xx candidate, drop any stale error body so the detail page doesn't
	// show a previous candidate's error as this (successful) request's
	// "upstream response".
	rc.clearResponseBodies()
	usage, err := passthroughStreamToClient(c, resp, rc)
	// passthroughStreamToClient deliberately leaves the capture file open past
	// its own return, so the writeStreamErrorEvent call below can still
	// append to it; this function is the single place responsible for
	// closing it, on every exit path.
	defer closeStreamBodyFile(rc)
	if usage != nil {
		rc.Usage = usage // preserve partial usage even on error paths
	}
	// If the stream never produced a sent byte, the capture file (if any)
	// is empty — remove it so the detail page never shows an empty "stream
	// body" link (covers both the pre-first-byte failover path below and an
	// early client-disconnect).
	removeEmptyStreamBodyFile(c, rc)
	if err == nil {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptSuccess, ""))
		s.finalize(rc, http.StatusOK, "", start)
		return attemptSuccess
	}
	if errors.Is(err, errClientDisconnected) {
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptConnError, "client disconnected"))
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
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream ended without [DONE]"))
		s.finalize(rc, http.StatusOK, "stream_no_done", start)
		return attemptSuccess
	}
	if rc.FirstByteSent {
		// Mid-stream failure after the first byte — can't switch,
		// can't change HTTP status; emit one inline SSE error event + close.
		writeStreamErrorEvent(c, rc)
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream mid: "+err.Error()))
		s.finalize(rc, http.StatusOK, "stream_partial: "+err.Error(), start)
		return attemptSuccess
	}
	// Pre-first-byte failure: nothing written yet, can still failover.
	rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, resp.StatusCode, AttemptServerError, "stream start: "+err.Error()))
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
// rc.FirstByteSent flips true and no more switching is allowed.
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
func passthroughStreamToClient(c *gin.Context, resp *http.Response, rc *RelayContext) (*Usage, error) {
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

	flusher, _ := c.Writer.(http.Flusher)
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
			// Flush to the client BEFORE the capture-file append for
			// efficiency: the append does a redact pass (regexes +
			// a conditional JSON decode/marshal) plus a disk write, neither
			// of which the caller should wait on before seeing bytes it
			// already received — capture is best-effort persistence, not
			// part of the client-visible streaming path.
			sent := forwardStreamLine(c, rc, line, &usage, &doneSeen)
			if flusher != nil {
				flusher.Flush()
			}
			appendStreamBodyLine(rc, sent)
		case isDataLine(line):
			// First data frame — commit the SSE headers, flush any buffered
			// preamble in order, then forward the data line.
			writeSSEHeader(c)
			hadPreamble := len(preamble) > 0
			if hadPreamble {
				_, _ = c.Writer.Write(preamble)
			}
			headerWritten = true
			sent := forwardStreamLine(c, rc, line, &usage, &doneSeen)
			if flusher != nil {
				flusher.Flush()
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
