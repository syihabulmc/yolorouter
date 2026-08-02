package responses

import (
	"encoding/json"
	"fmt"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// ResponseDecoder parses a non-streaming OpenAI Responses API response into IR.
// Used for decoding responses after any client protocol has routed to a Responses upstream.
type ResponseDecoder struct{}

// responsesResponseWire mirrors the wire format of a Responses API response (for decoding).
// Kept unexported: protocol-layer wire types are not exposed outside the package; callers
// only ever see IR.
type responsesResponseWire struct {
	ID                string                   `json:"id"`
	Object            string                   `json:"object"`
	Model             string                   `json:"model"`
	Status            string                   `json:"status"`
	Output            []responsesOutputItem    `json:"output"`
	Usage             *responsesUsage          `json:"usage,omitempty"`
	IncompleteDetails *responsesIncompleteWire `json:"incomplete_details,omitempty"`
	Error             *responsesErrorWire      `json:"error,omitempty"`
}

type responsesOutputItem struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id,omitempty"`
	Role      string                 `json:"role,omitempty"`
	Content   []responsesPartWire    `json:"content,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Summary   []responsesSummaryWire `json:"summary,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments string                 `json:"arguments,omitempty"`
}

type responsesPartWire struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type responsesSummaryWire struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesUsage struct {
	InputTokens         int                     `json:"input_tokens"`
	OutputTokens        int                     `json:"output_tokens"`
	TotalTokens         int                     `json:"total_tokens"`
	InputTokensDetails  *responsesInputDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *responsesOutputDetails `json:"output_tokens_details,omitempty"`
	// Non-standard cache-write breakdown; see protocols.CacheWriteAliasField.
	// Like cached_tokens it sits INSIDE input_tokens, so it is recorded rather
	// than added.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type responsesInputDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	// Cache WRITE, nested beside cached_tokens. This is OpenAI's OWN field, not
	// an extension: the official OpenAPI spec declares
	// ResponseUsage.input_tokens_details.cache_write_tokens and lists it as
	// REQUIRED. An earlier revision of this comment called it an extension —
	// that was wrong, and the mistake came from checking a third party's
	// rendition of the schema instead of the vendor spec itself.
	//
	// A pointer so "field absent" is distinguishable from "field present and
	// 0"; precedence over the top-level alias is by presence, not by value.
	CacheWriteTokens *int `json:"cache_write_tokens"`
}

type responsesOutputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type responsesIncompleteWire struct {
	Reason string `json:"reason"`
}

type responsesErrorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ResponseDecoder) DecodeResponse(body json.RawMessage) (*protocols.IRResponse, error) {
	var wire responsesResponseWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("parse responses response: %w", err)
	}

	// The upstream may declare status=failed or a non-nil error inside an HTTP 200 body
	// (this is allowed by the Responses API spec). Failing to detect this would let a
	// failed response get encoded to the client as if it succeeded -> the circuit breaker
	// would record a success and it would be billed as a 2xx. Even on failure we still
	// extract wire.Usage (if the upstream sent it) so provider-cost analytics don't lose
	// data, then return (partialResp, error) so the caller's IR pipeline routes it into the
	// failure path (502, not billed).
	if wire.Error != nil || wire.Status == "failed" {
		partial := protocols.NewIRResponse(wire.ID, wire.Model)
		if wire.Usage != nil {
			partial.Usage.PromptTokens = wire.Usage.InputTokens
			partial.Usage.CompletionTokens = wire.Usage.OutputTokens
			partial.Usage.TotalTokens = wire.Usage.TotalTokens
		}
		if wire.Error != nil {
			return partial, fmt.Errorf("upstream responses returned error: code=%q message=%q", wire.Error.Code, wire.Error.Message)
		}
		return partial, fmt.Errorf("upstream responses status=failed")
	}

	resp := protocols.NewIRResponse(wire.ID, wire.Model)

	// Walk the output array, concatenating text/reasoning content and collecting tool calls.
	var textBuf, reasoningBuf string
	for _, item := range wire.Output {
		switch item.Type {
		case "message":
			for _, p := range item.Content {
				switch p.Type {
				case "output_text":
					textBuf += p.Text
				case "refusal":
					textBuf += p.Refusal
				}
			}
		case "reasoning":
			for _, s := range item.Summary {
				if s.Type == "summary_text" || s.Type == "" {
					reasoningBuf += s.Text
				}
			}
		case "function_call":
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			resp.ToolCalls = append(resp.ToolCalls, protocols.IRToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		}
	}
	resp.Content = textBuf
	resp.ReasoningContent = reasoningBuf

	// usage
	if wire.Usage != nil {
		resp.Usage.PromptTokens = wire.Usage.InputTokens
		resp.Usage.CompletionTokens = wire.Usage.OutputTokens
		resp.Usage.TotalTokens = wire.Usage.TotalTokens
		if wire.Usage.InputTokensDetails != nil && wire.Usage.InputTokensDetails.CachedTokens > 0 {
			resp.Usage.CacheReadTokens = wire.Usage.InputTokensDetails.CachedTokens
			resp.Usage.CacheIncludedInPrompt = true
		}
		// Exactly one cache-write spelling is taken, never summed: the nested
		// breakdown is the standard one and wins, the top-level alias is the
		// fallback for new-api-style peers. Precedence is by field PRESENCE, so
		// an explicit 0 beats a stale non-zero alias.
		//
		// Neither is gated on being positive: a negative count from a buggy or
		// hostile upstream has to reach the gateway's coherence check, which
		// rejects the whole record as unknown. Masking it to 0 instead would
		// let the remaining counts be billed as though nothing were wrong.
		resp.Usage.CacheWriteTokens = wire.Usage.CacheCreationInputTokens
		if wire.Usage.InputTokensDetails != nil && wire.Usage.InputTokensDetails.CacheWriteTokens != nil {
			resp.Usage.CacheWriteTokens = *wire.Usage.InputTokensDetails.CacheWriteTokens
		}
		// The flag, by contrast, is only meaningful for a real count. The write
		// sits inside input_tokens, so without it NetPromptTokens would skip
		// the subtraction on a write-only request and over-report fresh input.
		if resp.Usage.CacheWriteTokens > 0 {
			resp.Usage.CacheIncludedInPrompt = true
		}
		if wire.Usage.OutputTokensDetails != nil {
			resp.Usage.ReasoningTokens = wire.Usage.OutputTokensDetails.ReasoningTokens
		}
	}

	// Infer stop reason: when status is "incomplete", prefer incomplete_details.reason;
	// otherwise return "tool_use" if there are tool calls; otherwise leave it empty
	// (the client encoder decides the default value).
	if wire.Status == "incomplete" && wire.IncompleteDetails != nil {
		resp.StopReason = wire.IncompleteDetails.Reason
	} else if len(resp.ToolCalls) > 0 {
		resp.StopReason = "tool_use"
	} else if wire.Status == "completed" {
		resp.StopReason = "stop"
	}

	return resp, nil
}
