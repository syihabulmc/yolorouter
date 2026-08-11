// Package modelname puts the caller's own name for a model back into the
// response they read.
//
// A caller asks for a model by the name the operator publishes. The provider
// serving it knows it by a different name, and echoes that one in everything it
// returns. Left alone it reaches the caller, who then sends it back on the next
// turn — a name the gateway does not route, from a client that was only
// repeating what it was told.
//
// This is a codec wrapper because a converted response is built by an encoder,
// and the name is one field in what that encoder is given. It applies only
// where a conversion happens: a response relayed in the caller's own protocol
// never passes through an encoder, and the kernel rewrites those bytes itself.
package modelname

import (
	"encoding/json"
	"slices"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// View is what this capability needs of the exchange: the name the caller used.
type View interface {
	OriginalModel() string
}

// Rewriter is the capability. It holds no per-request state.
type Rewriter struct{}

// New builds the capability.
func New() *Rewriter { return &Rewriter{} }

func (*Rewriter) Name() string { return "model_name" }

// Applies reports whether there is a name to put back. There is not when the
// exchange never resolved one, and writing an empty string over the provider's
// name would leave the caller with no name at all — worse than the wrong one.
func (*Rewriter) Applies(view View) bool { return view.OriginalModel() != "" }

// WrapResponseEncoder renames the model on a non-streaming response.
func (*Rewriter) WrapResponseEncoder(view View, enc protocols.ResponseEncoder, _ fact.Sink) protocols.ResponseEncoder {
	return responseEncoder{inner: enc, model: view.OriginalModel()}
}

// WrapStreamEncoder renames the model on the one event of a stream that carries
// it.
func (*Rewriter) WrapStreamEncoder(view View, enc protocols.StreamEncoder, _ fact.Sink) protocols.StreamEncoder {
	return streamEncoder{inner: enc, model: view.OriginalModel()}
}

type responseEncoder struct {
	inner protocols.ResponseEncoder
	model string
}

func (e responseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	if resp == nil {
		return e.inner.EncodeResponse(resp)
	}
	// A copy, not an edit. The caller still holds this response, and the name it
	// carries is the provider's own — the right answer for anything still
	// reasoning about which provider served the request. A wrapper whose job is
	// to format output does not get to change the input behind its caller.
	renamed := *resp
	renamed.Model = e.model
	return e.inner.EncodeResponse(&renamed)
}

type streamEncoder struct {
	inner protocols.StreamEncoder
	model string
}

func (e streamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	// Cloned for the same reason the response encoder copies: writing back into
	// the caller's slice would rename the frames they still hold. The slice is
	// one pump iteration's worth of deltas, so the copy costs nothing worth
	// weighing against a wrapper reaching backwards into its caller.
	renamed := slices.Clone(deltas)
	for i, d := range renamed {
		if ms, ok := d.(protocols.DeltaMessageStart); ok {
			ms.Model = e.model
			renamed[i] = ms
		}
	}
	return e.inner.EncodeDeltas(renamed)
}

func (e streamEncoder) EncodeDone() []protocols.SSEEvent { return e.inner.EncodeDone() }

func (e streamEncoder) Usage() protocols.IRUsage { return e.inner.Usage() }
