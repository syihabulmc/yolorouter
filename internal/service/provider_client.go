// ProviderClient abstracts the outbound "test a provider connection" call
// so provider_service.go can be unit-tested with a fake
// implementation, never triggering a real network request.
package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yolorouter/yolorouter/internal/middleware"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
	"github.com/yolorouter/yolorouter/internal/protocols/gemini"
	"github.com/yolorouter/yolorouter/internal/protocols/responses"
	"github.com/yolorouter/yolorouter/internal/service/safehttp"
)

// TestOutcome is one of the 9 test-result categories. Its
// numeric values are stored verbatim in provider_keys.last_test_result
// (see model.LastTestResult* constants, which must stay numerically
// identical to this list).
type TestOutcome int

const (
	TestSuccess TestOutcome = iota
	TestAuthFailed
	TestPermissionDenied
	TestModelNotFound
	TestQuotaUnavailable
	TestRateLimited
	TestUnreachable
	TestUpstreamError
	// TestVerificationUnsupported marks a destination whose protocol has no
	// real success-body validator yet (gemini/responses — see
	// isParseableJSONObjectWithoutError's comment): a 2xx response from that
	// destination cannot be certified as a genuine pass, so this outcome is
	// returned instead of TestSuccess. classifyTestResult never lets this
	// outcome mark a key passed/enabled, unlike a real TestSuccess.
	TestVerificationUnsupported
)

// TestResult is what ProviderClient returns for one test attempt.
type TestResult struct {
	Outcome    TestOutcome
	DurationMs int64
	// IsModelScoped is only meaningful when Outcome == TestPermissionDenied:
	// true if the response body structurally names the tested model as the
	// reason (error.param == "model", or the message references it). The
	// service layer's verification_status write rules
	// depend on this bit and never re-parse the raw HTTP body themselves.
	IsModelScoped bool
}

// ProviderClient is implemented by HTTPProviderClient in production and by
// a fake in provider_service_test.go. Every method takes the wire protocol
// the provider's credential must be tested against (openai/anthropic/
// gemini/responses) — a provider configured as an anthropic upstream must be
// probed with /v1/messages + x-api-key, never OpenAI's /chat/completions +
// Bearer token, or the "test" would exercise a completely different
// endpoint than production traffic actually dispatches to.
type ProviderClient interface {
	TestChatCompletion(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error)
	// TestStreamingCompletion validates that baseURL+model can serve a
	// streaming response:
	// success requires at least one structurally valid `delta` chunk
	// followed by a normal `data: [DONE]` termination.
	TestStreamingCompletion(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error)
	// TestFunctionCalling validates that baseURL+model can return a
	// structurally valid tool_calls response to a minimal tool definition.
	TestFunctionCalling(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error)
}

const (
	providerClientTimeout      = 15 * time.Second
	providerClientMaxBodyBytes = 64 * 1024
	// providerClientConcurrency caps simultaneous in-flight real provider
	// test calls across the whole process — chosen
	// generously enough for a single admin clicking several test buttons in
	// quick succession or one batch test, without letting an unbounded
	// number of outbound sockets accumulate.
	providerClientConcurrency = 8
)

// HTTPProviderClient is the production ProviderClient: a real minimal
// protocol-appropriate call through safehttp's SSRF-safe transport, with a
// hard per-call timeout and a shared concurrency cap.
type HTTPProviderClient struct {
	httpClient *http.Client
	limiter    *middleware.Semaphore
}

// NewHTTPProviderClient builds the connection-test client. allowPrivate is
// forwarded to safehttp.NewTransport so a self-hosted operator testing a
// LAN/localhost model server can opt out of the SSRF IP-range denial (see
// config.SecurityConfig.AllowPrivateUpstreams).
func NewHTTPProviderClient(allowPrivate bool) *HTTPProviderClient {
	return &HTTPProviderClient{
		httpClient: &http.Client{
			Transport: safehttp.NewTransport(allowPrivate),
			// Never follow redirects. Without this,
			// Go's default http.Client follows up to 10 redirect hops and
			// may carry the credential header (the decrypted upstream
			// key) to a host the admin never confirmed.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		limiter: middleware.NewSemaphore(providerClientConcurrency),
	}
}

// requestEncoderFor returns the codec whose EgressPath/SetupRequest a
// credential test must drive for proto — the exact same encoders runtime
// dispatch uses, so a provider that passes its test is guaranteed to be
// hit the same way in production. Unknown protocols fall back to the
// OpenAI codec, mirroring protocolForProviderType's own default.
func requestEncoderFor(proto protocols.ProtocolID) protocols.RequestEncoder {
	switch proto {
	case protocols.ProtocolClaude:
		return claude.RequestEncoder{}
	case protocols.ProtocolGemini:
		return gemini.RequestEncoder{}
	case protocols.ProtocolResponses:
		return responses.RequestEncoder{}
	default:
		return chat.RequestEncoder{}
	}
}

// protocolForProviderType maps a provider_type column value to the wire
// protocol its credential test must speak. Empty/unknown values normalize
// to OpenAI, matching ValidateProviderType's own default for backward
// compatibility with providers created before provider_type existed.
func protocolForProviderType(providerType string) protocols.ProtocolID {
	switch protocols.ProtocolID(providerType) {
	case protocols.ProtocolClaude, protocols.ProtocolGemini, protocols.ProtocolResponses:
		return protocols.ProtocolID(providerType)
	default:
		return protocols.ProtocolOpenAI
	}
}

// chatCompletionErrorBody and chatCompletionSuccessBody are OpenAI Chat
// Completions body shapes.
type chatCompletionErrorBody struct {
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Param   string `json:"param"`
	} `json:"error"`
}

type chatCompletionSuccessBody struct {
	Choices []struct {
		// Message is a pointer so a body that omits the field entirely, or
		// sets it explicitly to null, both decode to nil — isValidOpenAIChatSuccessBody
		// treats either as "not actually a valid completion", not a
		// zero-value message that would otherwise satisfy len(Choices) > 0.
		Message *struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// claudeErrorBody and claudeSuccessBody are Anthropic Messages API body
// shapes: a success body carries "type":"message" with a non-empty content
// array, an error body carries "type":"error" with a nested error object.
type claudeErrorBody struct {
	Type  string `json:"type"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type claudeSuccessBody struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
	} `json:"content"`
}

// providerTestURL builds the connection-test endpoint URL using the exact
// same builder runtime dispatch uses (protocols.JoinUpstreamURL with the
// protocol's own egress path) — otherwise a bare-host or path-prefixed
// baseURL could pass verification against one endpoint while production
// traffic actually dispatches to another (e.g. a bare host silently NOT
// getting /v1 inserted at verification time while runtime dispatch does
// insert it). model is passed through to EgressPath (rather than an empty
// string) because Gemini's egress path embeds the model name
// (/models/X:generateContent); every other codec here ignores it.
func providerTestURL(baseURL string, proto protocols.ProtocolID, model string) string {
	return protocols.JoinUpstreamURL(baseURL, requestEncoderFor(proto).EgressPath(model, false), proto)
}

// runTestRequest builds and sends a POST request against the protocol's own
// test endpoint, holding
// the shared concurrency slot and per-call timeout for the request's ENTIRE
// duration — including whatever handle does with the response body — not
// just until headers arrive. This is why the semaphore/timeout/transport-
// error handling can't be split into a plain "get me an *http.Response"
// helper that returns before the caller reads the body: streaming's body
// read can itself take a while, and it must still count against the same
// cap as every other in-flight test call.
//
// On a transport-level failure (network/timeout/SSRF-blocked dial), handle
// is never invoked and TestUnreachable is returned directly, matching the
// "don't leak which kind of failure this was" rule.
func (c *HTTPProviderClient) runTestRequest(
	ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string, reqPayload interface{},
	handle func(resp *http.Response, durationMs int64) (TestResult, error),
) (TestResult, error) {
	if !c.limiter.TryAcquire() {
		return TestResult{}, fmt.Errorf("too many concurrent provider test calls in flight")
	}
	defer c.limiter.Release()

	ctx, cancel := context.WithTimeout(ctx, providerClientTimeout)
	defer cancel()

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return TestResult{}, fmt.Errorf("marshal request body: %w", err)
	}

	url := providerTestURL(baseURL, proto, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return TestResult{}, fmt.Errorf("build request: %w", err)
	}
	// The codec sets Content-Type plus the protocol-correct auth header:
	// Authorization: Bearer for openai/responses, x-api-key +
	// anthropic-version for anthropic, an API-key header/param for gemini.
	requestEncoderFor(proto).SetupRequest(req, apiKey)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{Outcome: TestUnreachable, DurationMs: duration}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	return handle(resp, duration)
}

// readBoundedBody applies the same "don't trust an unbounded upstream body"
// limit every test method needs before classifying a non-streaming response.
func readBoundedBody(resp *http.Response) ([]byte, bool) {
	limited := io.LimitReader(resp.Body, providerClientMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > providerClientMaxBodyBytes {
		return nil, false
	}
	return body, true
}

// chatCompletionPayload builds the minimal request body for a basic-text
// credential test, shaped for proto. anthropic requires max_tokens on every
// request. gemini/responses bodies are structurally minimal — a dedicated
// per-protocol body validator for these two is deferred; only their request
// shape (enough to avoid a spurious 400) is implemented here.
func chatCompletionPayload(proto protocols.ProtocolID, model string) map[string]interface{} {
	switch proto {
	case protocols.ProtocolClaude:
		return map[string]interface{}{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
	case protocols.ProtocolGemini:
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"role": "user", "parts": []map[string]string{{"text": "ping"}}},
			},
		}
	case protocols.ProtocolResponses:
		return map[string]interface{}{
			"model": model,
			"input": "ping",
		}
	default: // openai
		return map[string]interface{}{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
		}
	}
}

func (c *HTTPProviderClient) TestChatCompletion(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error) {
	payload := chatCompletionPayload(proto, model)
	return c.runTestRequest(ctx, proto, baseURL, apiKey, model, payload, func(resp *http.Response, duration int64) (TestResult, error) {
		body, ok := readBoundedBody(resp)
		if !ok {
			return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
		}
		return classifyResponse(proto, resp, body, model, duration), nil
	})
}

// streamChunk mirrors the minimal OpenAI streaming chunk shape this test
// needs to recognize a structurally valid delta — same "don't trust a 200
// status code alone" principle as isValidOpenAIChatSuccessBody. Only OpenAI
// SSE deltas are parsed this deeply; other protocols' stream shapes
// (Claude's content_block_delta events, etc.) are deferred — see
// TestStreamingCompletion.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// scanSSEStream reads a bounded number of bytes off r looking for at least
// one structurally valid `data: {...}` chunk followed by a normal
// `data: [DONE]` termination. Non-"data:" lines (blank lines, comments) are
// skipped, not treated as failures. This is OpenAI's own SSE termination
// convention (see TestStreamingCompletion for why it's not reused for other
// protocols).
func scanSSEStream(r io.Reader) (sawValidDelta, sawDone bool) {
	scanner := bufio.NewScanner(io.LimitReader(r, providerClientMaxBodyBytes))
	for scanner.Scan() {
		line := scanner.Text()
		data := strings.TrimPrefix(line, "data: ")
		if data == line {
			continue // not an SSE data line
		}
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			sawValidDelta = true
		}
	}
	return sawValidDelta, sawDone
}

// streamingCompletionPayload mirrors chatCompletionPayload with stream
// enabled where the protocol's wire format needs an explicit flag (openai/
// anthropic/responses); gemini's generateContent request body is the same
// either way, only the endpoint differs, but streaming egress for gemini is
// not modeled by any codec here yet, so the same non-streaming body is
// reused.
func streamingCompletionPayload(proto protocols.ProtocolID, model string) map[string]interface{} {
	switch proto {
	case protocols.ProtocolClaude:
		return map[string]interface{}{
			"model":      model,
			"max_tokens": 1,
			"stream":     true,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
	case protocols.ProtocolGemini:
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"role": "user", "parts": []map[string]string{{"text": "ping"}}},
			},
		}
	case protocols.ProtocolResponses:
		return map[string]interface{}{
			"model":  model,
			"input":  "ping",
			"stream": true,
		}
	default: // openai
		return map[string]interface{}{
			"model":    model,
			"stream":   true,
			"messages": []map[string]string{{"role": "user", "content": "ping"}},
		}
	}
}

func (c *HTTPProviderClient) TestStreamingCompletion(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error) {
	payload := streamingCompletionPayload(proto, model)
	return c.runTestRequest(ctx, proto, baseURL, apiKey, model, payload, func(resp *http.Response, duration int64) (TestResult, error) {
		if resp.StatusCode != http.StatusOK {
			body, ok := readBoundedBody(resp)
			if !ok {
				return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
			}
			return classifyResponse(proto, resp, body, model, duration), nil
		}

		if proto != protocols.ProtocolOpenAI {
			// Non-OpenAI SSE delta-shape validation (Claude's
			// content_block_delta/message_stop events, Gemini's chunked
			// generateContent stream, Responses' response.output_text.delta
			// events) is deferred to a follow-up. A 200 status with a
			// non-empty body is treated as a structurally-unverified pass —
			// this at least proves the endpoint/auth/payload shape didn't
			// get rejected outright, matching this task's "wire the
			// mechanism, refine body classification later" scope for these
			// protocols.
			body, ok := readBoundedBody(resp)
			if !ok || len(body) == 0 {
				return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
			}
			return TestResult{Outcome: TestSuccess, DurationMs: duration}, nil
		}

		sawValidDelta, sawDone := scanSSEStream(resp.Body)
		if sawValidDelta && sawDone {
			return TestResult{Outcome: TestSuccess, DurationMs: duration}, nil
		}
		return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
	})
}

// toolCallResponseBody mirrors the minimal OpenAI tool_calls response shape.
type toolCallResponseBody struct {
	Choices []struct {
		Message struct {
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// isValidToolCallsBody requires at least one choice with at least one
// tool_calls entry naming a real function and carrying parseable JSON
// arguments — a bare non-empty tool_calls array with garbage fields is not
// enough (mirrors isValidOpenAIChatSuccessBody's "don't trust the status
// code alone" principle, applied to the tool-call response shape). This is
// OpenAI's own tool_calls shape; see TestFunctionCalling for other
// protocols.
func isValidToolCallsBody(body []byte) bool {
	var parsed toolCallResponseBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	if len(parsed.Choices) == 0 || len(parsed.Choices[0].Message.ToolCalls) == 0 {
		return false
	}
	call := parsed.Choices[0].Message.ToolCalls[0]
	if call.Function.Name == "" {
		return false
	}
	var args map[string]interface{}
	return json.Unmarshal([]byte(call.Function.Arguments), &args) == nil
}

// weatherToolDescription/weatherToolParameters are the shared "get_weather"
// tool definition content reused (in each protocol's own shape) across
// functionCallingPayload's branches.
const (
	weatherToolName        = "get_weather"
	weatherToolDescription = "Get the current weather for a location"
)

func weatherToolParameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"location": map[string]string{"type": "string"}},
		"required":   []string{"location"},
	}
}

// functionCallingPayload builds the minimal tool-calling credential test
// body per protocol. gemini/responses shapes are structurally minimal
// (enough to avoid a spurious 400); their tool_calls-equivalent response
// body is not parsed by this test yet — see TestFunctionCalling.
func functionCallingPayload(proto protocols.ProtocolID, model string) map[string]interface{} {
	switch proto {
	case protocols.ProtocolClaude:
		return map[string]interface{}{
			"model":      model,
			"max_tokens": 64,
			"messages": []map[string]string{
				{"role": "user", "content": "What's the weather in Beijing?"},
			},
			"tools": []map[string]interface{}{
				{
					"name":         weatherToolName,
					"description":  weatherToolDescription,
					"input_schema": weatherToolParameters(),
				},
			},
		}
	case protocols.ProtocolGemini:
		return map[string]interface{}{
			"contents": []map[string]interface{}{
				{"role": "user", "parts": []map[string]string{{"text": "What's the weather in Beijing?"}}},
			},
			"tools": []map[string]interface{}{
				{
					"functionDeclarations": []map[string]interface{}{
						{
							"name":        weatherToolName,
							"description": weatherToolDescription,
							"parameters":  weatherToolParameters(),
						},
					},
				},
			},
		}
	case protocols.ProtocolResponses:
		return map[string]interface{}{
			"model": model,
			"input": "What's the weather in Beijing?",
			"tools": []map[string]interface{}{
				{
					"type":        "function",
					"name":        weatherToolName,
					"description": weatherToolDescription,
					"parameters":  weatherToolParameters(),
				},
			},
		}
	default: // openai
		return map[string]interface{}{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": "What's the weather in Beijing?"},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        weatherToolName,
						"description": weatherToolDescription,
						"parameters":  weatherToolParameters(),
					},
				},
			},
		}
	}
}

func (c *HTTPProviderClient) TestFunctionCalling(ctx context.Context, proto protocols.ProtocolID, baseURL, apiKey, model string) (TestResult, error) {
	payload := functionCallingPayload(proto, model)
	return c.runTestRequest(ctx, proto, baseURL, apiKey, model, payload, func(resp *http.Response, duration int64) (TestResult, error) {
		body, ok := readBoundedBody(resp)
		if !ok {
			return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
		}
		if resp.StatusCode != http.StatusOK {
			return classifyResponse(proto, resp, body, model, duration), nil
		}
		if proto != protocols.ProtocolOpenAI {
			// Non-OpenAI tool-call body shapes (Claude's
			// content[].type=="tool_use" blocks, Gemini's functionCall
			// parts) are deferred; a parseable, error-free 200 body is
			// treated as a structurally-unverified pass here — see
			// functionCallingPayload's comment for the same deferral on the
			// request side.
			if !isParseableJSONObjectWithoutError(body) {
				return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
			}
			return TestResult{Outcome: TestSuccess, DurationMs: duration}, nil
		}
		if !isValidToolCallsBody(body) {
			return TestResult{Outcome: TestUpstreamError, DurationMs: duration}, nil
		}
		return TestResult{Outcome: TestSuccess, DurationMs: duration}, nil
	})
}

// classifyResponse maps an HTTP response into one of the 9 TestOutcome
// categories. The status-code switch itself is protocol-neutral — every
// protocol here uses the same HTTP status conventions (401 auth, 403
// permission, 404 not-found, 429 rate/quota, 5xx upstream) — only the
// body-shape predicates it calls out to (isValidSuccessBody,
// isModelScopedError, isQuotaError, isModelNotFoundError) branch on proto.
func classifyResponse(proto protocols.ProtocolID, resp *http.Response, body []byte, model string, durationMs int64) TestResult {
	switch resp.StatusCode {
	case http.StatusOK:
		if proto == protocols.ProtocolGemini || proto == protocols.ProtocolResponses {
			// gemini/responses have no real success-body validator yet (see
			// isParseableJSONObjectWithoutError's comment) — a 2xx here only
			// proves the request wasn't rejected outright, not that the
			// credential actually works. Treating that as TestSuccess would
			// let a key be authorized against a destination that was never
			// truly verified, so it is reported as "cannot certify yet"
			// instead; real error statuses below stay meaningful for these
			// protocols too.
			return TestResult{Outcome: TestVerificationUnsupported, DurationMs: durationMs}
		}
		if isValidSuccessBody(proto, resp, body) {
			return TestResult{Outcome: TestSuccess, DurationMs: durationMs}
		}
		return TestResult{Outcome: TestUpstreamError, DurationMs: durationMs}
	case http.StatusUnauthorized:
		return TestResult{Outcome: TestAuthFailed, DurationMs: durationMs}
	case http.StatusForbidden:
		modelScoped := isModelScopedError(proto, body, model)
		return TestResult{Outcome: TestPermissionDenied, DurationMs: durationMs, IsModelScoped: modelScoped}
	case http.StatusNotFound:
		return TestResult{Outcome: TestModelNotFound, DurationMs: durationMs}
	case http.StatusTooManyRequests:
		if isQuotaError(proto, body) {
			return TestResult{Outcome: TestQuotaUnavailable, DurationMs: durationMs}
		}
		return TestResult{Outcome: TestRateLimited, DurationMs: durationMs}
	default:
		if resp.StatusCode >= 500 {
			return TestResult{Outcome: TestUpstreamError, DurationMs: durationMs}
		}
		if isModelNotFoundError(proto, body) {
			return TestResult{Outcome: TestModelNotFound, DurationMs: durationMs}
		}
		return TestResult{Outcome: TestUpstreamError, DurationMs: durationMs}
	}
}

// isValidSuccessBody enforces the "success cannot be judged by the status
// code alone" rule, branching to the protocol-appropriate body-shape check.
// classifyResponse's own StatusOK case now intercepts gemini/responses
// before ever calling this function (see its comment), so the
// ProtocolGemini/ProtocolResponses branch below is unreachable from that one
// call site — it stays purely for direct callers (tests) that still want the
// old "parseable JSON, no top-level error" leniency check on its own.
func isValidSuccessBody(proto protocols.ProtocolID, resp *http.Response, body []byte) bool {
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "application/json") {
		return false
	}
	switch proto {
	case protocols.ProtocolClaude:
		return isValidClaudeSuccessBody(body)
	case protocols.ProtocolGemini, protocols.ProtocolResponses:
		// Body-shape validation for these two is deferred (see the payload
		// builders' comments): a parseable JSON object carrying no
		// top-level "error" field is accepted as success.
		return isParseableJSONObjectWithoutError(body)
	default: // openai
		return isValidOpenAIChatSuccessBody(body)
	}
}

// isValidOpenAIChatSuccessBody is the original OpenAI Chat Completions
// success-body check: the body must parse, carry no top-level error, and
// have at least one choice with a non-null message.
func isValidOpenAIChatSuccessBody(body []byte) bool {
	var errBody chatCompletionErrorBody
	if err := json.Unmarshal(body, &errBody); err == nil && errBody.Error != nil {
		return false
	}
	var success chatCompletionSuccessBody
	if err := json.Unmarshal(body, &success); err != nil {
		return false
	}
	return len(success.Choices) > 0 && success.Choices[0].Message != nil
}

// isValidClaudeSuccessBody requires the Anthropic Messages API success
// shape: "type":"message" with a non-empty content array. A "type":"error"
// body (or anything else) is rejected outright by the type check.
func isValidClaudeSuccessBody(body []byte) bool {
	var success claudeSuccessBody
	if err := json.Unmarshal(body, &success); err != nil {
		return false
	}
	return success.Type == "message" && len(success.Content) > 0
}

// isParseableJSONObjectWithoutError is the basic gemini/responses success
// check: the body must parse as a JSON object and carry no top-level
// "error" field. It intentionally does not validate any deeper structure —
// see isValidSuccessBody's comment. Still used directly by
// TestFunctionCalling's non-openai 200 path (tool-call body classification
// for these protocols remains a separate, still-deferred concern from
// TestChatCompletion's TestVerificationUnsupported classification above).
func isParseableJSONObjectWithoutError(body []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	_, hasError := obj["error"]
	return !hasError
}

func parseErrorBody(body []byte) *chatCompletionErrorBody {
	var parsed chatCompletionErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	return &parsed
}

func parseClaudeErrorBody(body []byte) *claudeErrorBody {
	var parsed claudeErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	return &parsed
}

// isModelScopedError reports whether a 403 body structurally names the
// tested model as the reason. OpenAI carries a structured error.param
// field for this; Anthropic doesn't, so only the message-text heuristic
// applies there. gemini/responses body parsing is deferred, so this always
// reports false for them — IsModelScoped only affects verification_status
// write rules, never whether the test itself passed or failed.
func isModelScopedError(proto protocols.ProtocolID, body []byte, model string) bool {
	switch proto {
	case protocols.ProtocolClaude:
		parsed := parseClaudeErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		return model != "" && strings.Contains(strings.ToLower(parsed.Error.Message), strings.ToLower(model))
	case protocols.ProtocolGemini, protocols.ProtocolResponses:
		return false
	default: // openai
		parsed := parseErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		if parsed.Error.Param == "model" {
			return true
		}
		return model != "" && strings.Contains(strings.ToLower(parsed.Error.Message), strings.ToLower(model))
	}
}

func isQuotaError(proto protocols.ProtocolID, body []byte) bool {
	switch proto {
	case protocols.ProtocolClaude:
		parsed := parseClaudeErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		lower := strings.ToLower(parsed.Error.Message)
		return strings.Contains(lower, "quota") || strings.Contains(lower, "billing") || strings.Contains(lower, "credit")
	case protocols.ProtocolGemini, protocols.ProtocolResponses:
		// Body parsing deferred: a 429 for these protocols always
		// classifies as TestRateLimited via classifyResponse's default
		// fallback rather than TestQuotaUnavailable.
		return false
	default: // openai
		parsed := parseErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		if parsed.Error.Code == "insufficient_quota" {
			return true
		}
		lower := strings.ToLower(parsed.Error.Message)
		return strings.Contains(lower, "quota") || strings.Contains(lower, "billing")
	}
}

func isModelNotFoundError(proto protocols.ProtocolID, body []byte) bool {
	switch proto {
	case protocols.ProtocolClaude:
		parsed := parseClaudeErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		lower := strings.ToLower(parsed.Error.Message)
		return strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"))
	case protocols.ProtocolGemini, protocols.ProtocolResponses:
		return false
	default: // openai
		parsed := parseErrorBody(body)
		if parsed == nil || parsed.Error == nil {
			return false
		}
		if parsed.Error.Code == "model_not_found" {
			return true
		}
		lower := strings.ToLower(parsed.Error.Message)
		return strings.Contains(lower, "model") && (strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"))
	}
}
