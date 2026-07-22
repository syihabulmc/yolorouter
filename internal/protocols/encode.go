package protocols

import (
	"encoding/json"
	"net/http"
)

// RequestEncoder encodes IR into a target protocol request.
type RequestEncoder interface {
	Protocol() ProtocolID
	EncodeRequest(req *IRRequest) (json.RawMessage, error)
	EgressPath(model string, stream bool) string
	SetupRequest(req *http.Request, apiKey string)
}

// ResponseEncoder encodes IR response into target protocol JSON.
type ResponseEncoder interface {
	EncodeResponse(resp *IRResponse) json.RawMessage
}

// StreamEncoder encodes IR deltas into target protocol SSE.
type StreamEncoder interface {
	EncodeDeltas(deltas []IRStreamDelta) []SSEEvent
	EncodeDone() []SSEEvent
	Usage() IRUsage
}

// SSEEvent represents a single SSE event.
type SSEEvent struct {
	Event string
	Data  string
}

func (e SSEEvent) String() string {
	if e.Event != "" {
		return "event: " + e.Event + "\ndata: " + e.Data + "\n\n"
	}
	return "data: " + e.Data + "\n\n"
}
