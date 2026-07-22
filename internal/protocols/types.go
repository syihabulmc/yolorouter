package protocols

import "encoding/json"

// --- Role ---

type Role int

const (
	RoleSystem Role = iota
	RoleUser
	RoleAssistant
	RoleTool
)

// --- Content Block ---

// IRContentBlock is a sealed interface for discriminated content types.
type IRContentBlock interface{ irContentBlock() }

type BlockText struct{ Text string }

func (BlockText) irContentBlock() {}

type BlockImage struct {
	MediaType string
	Data      string // base64 or URL
	IsURL     bool
}

func (BlockImage) irContentBlock() {}

type BlockAudio struct {
	MediaType string
	Data      string
	IsURL     bool
}

func (BlockAudio) irContentBlock() {}

type BlockThinking struct {
	Thinking  string
	Signature string
}

func (BlockThinking) irContentBlock() {}

type BlockRedactedThinking struct{ Data string }

func (BlockRedactedThinking) irContentBlock() {}

type BlockToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (BlockToolUse) irContentBlock() {}

type BlockToolResult struct {
	ToolUseID string
	Content   json.RawMessage
	IsError   bool
}

func (BlockToolResult) irContentBlock() {}

// --- Tool Call (OpenAI wire format) ---

type IRToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// --- Message ---

type IRMessage struct {
	Role       Role
	Content    []IRContentBlock
	ToolCalls  []IRToolCall
	ToolCallID string
	Meta       json.RawMessage
}

// --- Tool Spec ---

type IRToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      bool
}

type IRToolChoice int

const (
	ToolChoiceAuto IRToolChoice = iota
	ToolChoiceNone
	ToolChoiceRequired
	ToolChoiceNamed
)

// --- Generation Config ---

type IRGenerationConfig struct {
	Temperature       *float64
	MaxTokens         *int
	TopP              *float64
	TopK              *int
	TopA              *float64
	MinP              *float64
	StopSequences     []string
	Seed              *int64
	PresencePenalty   *float64
	FrequencyPenalty  *float64
	RepetitionPenalty *float64
	LogProbs          *bool
	TopLogProbs       *int
	// AllowExtendedParams is set by the Chat decoder when the ingress is OpenAI Chat protocol.
	// The Chat encoder only forwards non-standard extended params (top_k, top_a, min_p,
	// repetition_penalty) when this is true, preventing them from being sent to strict
	// OpenAI-compatible providers during cross-protocol conversion (e.g. Claude→Chat egress).
	// seed / logprobs / top_logprobs are standard OpenAI Chat params and always forwarded.
	AllowExtendedParams bool
}

// --- Reasoning Config ---

type IRReasoningConfig struct {
	Enabled         bool
	BudgetTokens    *int
	Effort          string // "low", "medium", "high"
	IncludeThoughts bool
}

// --- Response Format ---

type IRResponseFormat struct {
	Type   string
	Schema json.RawMessage
	Name   string
	Strict bool
}

// --- Stream Config ---

type IRStreamConfig struct {
	Enabled      bool
	IncludeUsage bool
}

// --- Safety Settings ---

type IRSafetySetting struct {
	Category  string
	Threshold string
}

// --- Vendor Bag ---

type IRVendorBag map[string]json.RawMessage

// --- IRRequest ---

type IRRequest struct {
	Model          string
	System         string
	Messages       []IRMessage
	Generation     IRGenerationConfig
	Stream         IRStreamConfig
	Tools          []IRToolSpec
	ToolChoice     IRToolChoice
	ToolChoiceName string
	Reasoning      IRReasoningConfig
	ResponseFormat *IRResponseFormat
	SafetySettings []IRSafetySetting
	Vendor         IRVendorBag
	SourceProtocol string
	RawBody        json.RawMessage
}

// --- IRResponse ---

type IRResponse struct {
	ID               string
	Model            string
	Content          string
	ReasoningContent string
	ToolCalls        []IRToolCall
	StopReason       string
	StopSequence     string // non-empty when StopReason == "stop_sequence"
	Usage            IRUsage
	Vendor           IRVendorBag
}

func NewIRResponse(id, model string) *IRResponse {
	return &IRResponse{ID: id, Model: model, Usage: IRUsage{CacheIncludedInPrompt: true}}
}
