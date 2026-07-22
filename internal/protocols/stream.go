package protocols

import "encoding/json"

// IRStreamDelta is a sealed interface for streaming deltas.
type IRStreamDelta interface{ irStreamDelta() }

type DeltaMessageStart struct {
	ID    string
	Model string
}

func (DeltaMessageStart) irStreamDelta() {}

type DeltaText struct{ Text string }

func (DeltaText) irStreamDelta() {}

type DeltaThinking struct{ Text string }

func (DeltaThinking) irStreamDelta() {}

type DeltaToolCallStart struct {
	Index int
	ID    string
	Name  string
}

func (DeltaToolCallStart) irStreamDelta() {}

type DeltaToolCallArgs struct {
	Index     int
	Arguments string
}

func (DeltaToolCallArgs) irStreamDelta() {}

type DeltaUsage struct{ Usage IRUsage }

func (DeltaUsage) irStreamDelta() {}

type DeltaDone struct {
	StopReason   string
	StopSequence string // non-empty when StopReason == "stop_sequence"
}

func (DeltaDone) irStreamDelta() {}

type DeltaUnknown struct{ Raw json.RawMessage }

func (DeltaUnknown) irStreamDelta() {}

// FinishSignals aggregates the termination signals of a streaming response, shared by
// multiple checkDone call sites.
type FinishSignals struct {
	Raw         string // DeltaDone.StopReason
	SawToolCall bool   // whether a tool call was observed (DeltaToolCallStart / DeltaToolCallArgs)
	Produced    bool   // whether any non-empty content was produced (DeltaText / DeltaThinking)
	SawDone     bool   // whether a DeltaDone was observed
}

// Accumulate processes a batch of deltas and updates the termination signal fields.
// It only handles finish_reason-related fields; merging DeltaUsage is the caller's responsibility.
func (s *FinishSignals) Accumulate(deltas []IRStreamDelta) {
	for _, d := range deltas {
		switch v := d.(type) {
		case DeltaDone:
			s.SawDone = true
			s.Raw = v.StopReason
		case DeltaToolCallStart:
			s.SawToolCall = true
		case DeltaToolCallArgs:
			s.SawToolCall = true
		case DeltaText:
			if v.Text != "" {
				s.Produced = true
			}
		case DeltaThinking:
			// A reasoning model may emit only thinking tokens while DeltaText stays empty;
			// this still counts as "produced", otherwise a normally completed
			// thinking-only response would be misclassified as empty.
			if v.Text != "" {
				s.Produced = true
			}
		}
	}
}
