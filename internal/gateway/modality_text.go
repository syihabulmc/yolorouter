package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// textModality serves the chat/completions family: JSON in, JSON or SSE out,
// counted in tokens. It is stateless and shared by every request.
type textModality struct{}

// NewTextModality returns the modality that serves text payloads.
//
// The return type is concrete, and the Modality/Payload assertions are absent,
// because Deliver has not moved yet: the delivery paths still live on the
// service, and claiming the interface before they arrive would be exactly the
// shell this whole step is meant not to produce — an adapter that names itself
// an adapter while the kernel still does its work. The assertions land with
// Deliver, and the compiler will then check the eight methods below against
// the same contract.
func NewTextModality() textModality { return textModality{} }

func (textModality) ID() ModalityID { return ModalityText }

// Limits asks for nothing of its own. Text has no frame that a chat line comes
// close to filling and no render that legitimately runs for minutes, so the
// kernel's own caps already describe it; a modality that repeated them here
// would be one more place for them to drift.
func (textModality) Limits() TransferLimits { return TransferLimits{} }

// Admit parses the caller's request far enough to route it, and refuses what
// no candidate could have served.
//
// Everything decided here is a property of the request alone — a body that
// does not parse, a model the caller did not name, a structure the protocol
// requires. Anything that depends on WHICH upstream ends up serving it belongs
// in Supports, where a refusal costs one candidate instead of the request.
func (textModality) Admit(_ context.Context, in Ingress) (*textPayload, *Rejection) {
	// Gemini carries neither model nor stream in the body: both live in the
	// path, so they have to be recovered before the peek can use them.
	var pathModel string
	var pathStream bool
	if in.Protocol == protocols.ProtocolGemini {
		m, s, ok := parseGeminiPath(in.Path)
		if !ok {
			return nil, &Rejection{
				Status: 400, ErrorType: errTypeInvalidRequest,
				Message: "invalid request path", FailReason: "invalid_gemini_path",
				Fault: fact.FaultClient,
			}
		}
		pathModel, pathStream = m, s
	}

	meta, err := peekIngress(in.Protocol, in.Body, pathModel, pathStream)
	if err != nil {
		return nil, &Rejection{
			Status: 400, ErrorType: errTypeInvalidRequest,
			Message: "invalid request body", FailReason: "parse: " + err.Error(),
			Fault: fact.FaultClient,
		}
	}
	if meta.Model == "" {
		return nil, &Rejection{
			Status: 400, ErrorType: errTypeInvalidRequest,
			Message: "model is required", FailReason: "empty_model",
			Fault: fact.FaultClient,
		}
	}
	if err := meta.validate(); err != nil {
		return nil, &Rejection{
			Status: 400, ErrorType: errTypeInvalidRequest,
			Message: err.Error(), FailReason: "validate: " + err.Error(),
			Fault: fact.FaultClient,
		}
	}
	// The full structural decode, run once here rather than per candidate.
	// Catching a malformed body now is what keeps it from surfacing later as a
	// misleading "all upstream candidates failed" after the chain has already
	// started.
	if err := validateIngressBody(in.Protocol, in.Body, meta.Model, meta.Stream); err != nil {
		return nil, &Rejection{
			Status: 400, ErrorType: errTypeInvalidRequest,
			Message:    "invalid request body: " + err.Error(),
			FailReason: "invalid_request: " + err.Error(),
			Fault:      fact.FaultClient,
		}
	}

	return &textPayload{ingress: in.Protocol, body: in.Body, meta: meta}, nil
}

// textPayload is one text request.
//
// It holds no kernel object on purpose. Everything it needs to reach the
// outside world arrives as DeliveryTools, which is what makes the seam real:
// a payload that could reach the exchange or the service would settle, bill
// and log for itself, and the eight entry points that each did exactly that
// are what this is replacing.
type textPayload struct {
	ingress protocols.ProtocolID
	body    []byte
	meta    *ingressMeta
}

func (p *textPayload) Routing() RoutingIntent {
	return RoutingIntent{Model: p.meta.Model, Stream: p.meta.Stream}
}

// EstimateCost says it cannot tell, which for text is the truth rather than a
// gap. How long the answer runs is the upstream's decision, and even the size
// of the question is not settled until the upstream reports how it counted the
// prompt. Modalities whose quantities are stated in the request — a number of
// images, a length of text to speak — are the ones this question exists for.
func (p *textPayload) EstimateCost(PricingView) CostEstimate {
	return CostEstimate{Known: false, Unit: fact.UnitToken}
}

// Supports accepts every candidate the kernel offers.
//
// Text has no per-candidate requirement the kernel does not already enforce:
// the protocol is negotiated for whichever provider is chosen, and a provider
// that cannot speak one is served through the intermediate representation
// instead, so there is no candidate a text request cannot be sent to.
//
// This is not the same as saying every candidate can serve it well. A request
// carrying tools sent to a candidate whose provider does not support function
// calling will fail upstream and fail over — which is today's behaviour, and
// changing it is a change to what the gateway does, not to how it is
// structured.
func (p *textPayload) Supports(Candidate) CandidateVerdict {
	return CandidateVerdict{OK: true}
}

// PrepareUpstream builds the body and path for one candidate.
//
// The result is only the modality's part of the request: the kernel adds the
// origin and the credentials, and runs whatever body rewriters are registered
// over these bytes afterwards. Rewriting is a capability that applies to every
// modality, so a modality that ran them itself would be one that could forget.
func (p *textPayload) PrepareUpstream(cand Candidate) (*UpstreamCall, error) {
	egress := codecsFor(cand.EgressProtocol)

	var out []byte
	if cand.Passthrough {
		rewritten, err := passthroughRequestBody(cand.EgressProtocol, p.body, cand.ProviderModelName)
		if err != nil {
			return nil, fmt.Errorf("rewrite model field: %w", err)
		}
		out = rewritten
	} else {
		ir, err := codecsFor(p.ingress).RequestDecoder.DecodeRequest(
			json.RawMessage(p.body), cand.ProviderModelName, p.meta.Stream)
		if err != nil {
			return nil, fmt.Errorf("decode ingress request (%s): %w", p.ingress, err)
		}
		encoded, err := egress.RequestEncoder.EncodeRequest(ir)
		if err != nil {
			return nil, fmt.Errorf("encode egress request (%s): %w", cand.EgressProtocol, err)
		}
		out = []byte(encoded)
	}

	if cand.EgressProtocol == protocols.ProtocolOpenAI {
		injected, err := EnsureStreamUsageInjection(out, p.meta.Stream, p.meta.WantsStreamUsage)
		if err != nil {
			return nil, fmt.Errorf("inject stream usage: %w", err)
		}
		out = injected
	}

	path := egress.RequestEncoder.EgressPath(cand.ProviderModelName, p.meta.Stream)
	if cand.EgressProtocol == protocols.ProtocolGemini && p.meta.Stream {
		path = strings.Replace(path, ":generateContent", ":streamGenerateContent?alt=sse", 1)
	}

	return &UpstreamCall{
		Path:        path,
		Body:        out,
		ContentType: "application/json",
		Progressive: false,
	}, nil
}

// NormalizeUpstreamError decides what the caller is told about an upstream
// failure.
//
// The provider's own wording never reaches the caller. An error body from an
// upstream can name the provider, the model behind the alias, or the account
// it was billed to, none of which the caller is entitled to know.
func (p *textPayload) NormalizeUpstreamError(status int, _ []byte, _ string) ErrorEnvelope {
	class := classifyUpstreamStatus(status)
	errType := class.ErrorType
	if errType == "" {
		errType = errTypeUpstream
	}
	return ErrorEnvelope{
		Status:    status,
		ErrorType: errType,
		Message:   safeUpstreamMessage(status),
	}
}

// FinalizeUsage hands back what the settled delivery reported.
//
// It reads the delivery rather than any running total the payload might keep,
// because the delivery is the one that belongs to the attempt being settled.
// A payload-level accumulator would survive an attempt that failed, and be
// charged under whatever ended the request.
func (p *textPayload) FinalizeUsage(d fact.Delivery) *fact.UsageReported {
	return d.Usage
}

// LogPolicy keeps all four bodies. They are JSON, they are what a protocol
// conversion bug has to be diagnosed from, and this table exists to hold them.
func (p *textPayload) LogPolicy() LogPolicy {
	return LogPolicy{Store: map[BodyKind]bool{
		BodyClientRequest:    true,
		BodyUpstreamRequest:  true,
		BodyUpstreamResponse: true,
		BodyClientResponse:   true,
	}}
}

// SanitizeForLog returns the bytes as they are.
//
// Text is the one modality for which that is safe, and saying so explicitly is
// the point of the method: the modalities that follow carry audio and image
// data that must never be stored as characters, and they cannot rely on the
// logger to know that.
func (p *textPayload) SanitizeForLog(_ BodyKind, _ string, body []byte) string {
	return string(body)
}
