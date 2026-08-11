package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// reportingWrapper reports one record when it is asked to wrap, so a test can
// look at what the timeline recorded about it.
type reportingWrapper struct{ name string }

func (w reportingWrapper) Name() string          { return w.name }
func (w reportingWrapper) Applies(struct{}) bool { return true }
func (w reportingWrapper) WrapResponseEncoder(_ struct{}, enc protocols.ResponseEncoder, sink fact.Sink) protocols.ResponseEncoder {
	sink.Note(fact.ModelRewritten{})
	return enc
}
func (w reportingWrapper) WrapStreamEncoder(_ struct{}, enc protocols.StreamEncoder, sink fact.Sink) protocols.StreamEncoder {
	sink.Note(fact.ModelRewritten{})
	return enc
}

type nopResponseEncoder struct{}

func (nopResponseEncoder) EncodeResponse(*protocols.IRResponse) json.RawMessage { return nil }

// Every other shape has the kernel stamp who reported a fact before handing
// over the sink. A wrapper reporting through an unstamped one lands on the
// timeline anonymous, which defeats the reason the timeline records provenance
// at all: an operator reading a fact has no way back to the code that said it.
func TestAFactReportedByACodecWrapperNamesTheWrapper(t *testing.T) {
	rc := &Exchange{requestID: "req-codec-provenance"}
	svc := &Service{}
	RegisterResponseCodecWrapper(svc, reportingWrapper{name: "the-wrapper"}, StageModelName,
		func(*Exchange) struct{} { return struct{}{} })

	ResponseCodecs{wrappers: svc.responseCodecWrappers, exchange: rc}.
		WrapResponse(nopResponseEncoder{})

	entries := rc.timeline.All()
	if len(entries) != 1 {
		t.Fatalf("timeline holds %d entries, want the one the wrapper reported", len(entries))
	}
	if entries[0].Reporter != "the-wrapper" {
		t.Fatalf("the fact is attributed to %q, want the wrapper that reported it", entries[0].Reporter)
	}
}

// deferredReporter keeps the sink it was handed and reports through it while
// encoding, which is the natural thing for this shape to do: a wrapper exists
// to act during encoding, not during wrapping.
type deferredReporter struct{ name string }

func (w deferredReporter) Name() string          { return w.name }
func (w deferredReporter) Applies(struct{}) bool { return true }

func (w deferredReporter) WrapResponseEncoder(_ struct{}, enc protocols.ResponseEncoder, sink fact.Sink) protocols.ResponseEncoder {
	return deferredEncoder{inner: enc, sink: sink}
}

func (w deferredReporter) WrapStreamEncoder(_ struct{}, enc protocols.StreamEncoder, _ fact.Sink) protocols.StreamEncoder {
	return enc
}

type deferredEncoder struct {
	inner protocols.ResponseEncoder
	sink  fact.Sink
}

func (e deferredEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	e.sink.Note(fact.ModelRewritten{})
	return e.inner.EncodeResponse(resp)
}

// Two wrappers, and the first keeps its sink to report later. By the time it
// does, wrapping has finished — so a sink shared across the chain and
// re-stamped as the loop advanced would file the first wrapper's report under
// the last wrapper's name, and an operator tracing the fact would land on code
// that never reported it.
func TestADeferredReportNamesTheWrapperThatMadeIt(t *testing.T) {
	rc := &Exchange{requestID: "req-deferred-provenance"}
	svc := &Service{}
	RegisterResponseCodecWrapper(svc, deferredReporter{name: "reports-late"}, StageModelName,
		func(*Exchange) struct{} { return struct{}{} })
	RegisterResponseCodecWrapper(svc, reportingWrapper{name: "wrapped-outermost"}, CodecStage(90),
		func(*Exchange) struct{} { return struct{}{} })

	enc := ResponseCodecs{wrappers: svc.responseCodecWrappers, exchange: rc}.
		WrapResponse(nopResponseEncoder{})
	// Nothing has encoded yet; the late report happens here.
	enc.EncodeResponse(&protocols.IRResponse{})

	var reporters []string
	for _, e := range rc.timeline.All() {
		reporters = append(reporters, e.Reporter)
	}
	if len(reporters) != 2 {
		t.Fatalf("timeline holds %v, want one report from each wrapper", reporters)
	}
	// The outermost wrapper reports while wrapping, so it lands first.
	if reporters[0] != "wrapped-outermost" || reporters[1] != "reports-late" {
		t.Fatalf("reports attributed to %v, want [wrapped-outermost reports-late]: a shared sink files the late one under the wrong name", reporters)
	}
}
