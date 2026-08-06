// This file is deliberately in package fact_test, not package fact.
//
// Everything it asserts is about what code OUTSIDE this package can do, and an
// in-package test cannot see that boundary: an unexported marker is reachable
// from within, so a test that lives beside the implementation would pass while
// every real caller failed to compile. The declarations below only build if the
// vocabulary is genuinely usable from elsewhere.
package fact_test

import (
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// deploymentRecord is what a capability in another package would declare to add
// an observation this package has never heard of. If this stops compiling, the
// recording vocabulary has closed, and the promise that a deployment can extend
// it without editing the kernel is broken — which is the whole reason the
// vocabulary is split in two.
type deploymentRecord struct {
	fact.Base
	Detail string
	Amount int64
}

func (deploymentRecord) RecordName() string { return "deployment_specific" }

// Compile-time assertion, stated separately from the tests so the failure names
// the property rather than whichever test happened to touch it first.
var _ fact.Record = deploymentRecord{}

// TestExternalPackageCanDeclareRecord proves an unfamiliar record survives a
// round trip through the timeline. A consumer that does not recognise the type
// must still be able to persist it under its name; the alternative — dropping
// it — is the failure this design exists to prevent.
func TestExternalPackageCanDeclareRecord(t *testing.T) {
	var tl fact.Timeline
	tl.Append(fact.Entry{
		Attempt:  1,
		At:       time.Unix(0, 0),
		Reporter: "some_capability",
		Record:   deploymentRecord{Detail: "d", Amount: 7},
	})

	entries := tl.All()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Record == nil {
		t.Fatal("record was dropped")
	}
	if got := entries[0].Record.RecordName(); got != "deployment_specific" {
		t.Errorf("RecordName = %q, want %q", got, "deployment_specific")
	}

	// A consumer recognises what it knows and falls through on the rest. The
	// default arm is the guarantee: an unknown record reaches it intact rather
	// than being lost on the way.
	switch r := entries[0].Record.(type) {
	case fact.UsageReported:
		t.Fatalf("unfamiliar record matched a known type: %#v", r)
	default:
		if r.RecordName() == "" {
			t.Error("unknown record reached the fallback without a usable name")
		}
	}
}

// TestKnownRecordsStillSatisfyRecord guards the other direction: exporting the
// marker must not have let a record type drift out of the interface.
func TestKnownRecordsStillSatisfyRecord(t *testing.T) {
	known := []fact.Record{
		fact.UsageReported{},
		fact.UsageIncoherent{},
		fact.TokensSaved{},
		fact.CompressionSkipped{},
		fact.SystemPromptInjected{},
		fact.ModelRewritten{},
		fact.FirstTokenAt{},
		fact.FinishReasonObserved{},
		fact.RateLimitSnapshot{},
	}
	seen := make(map[string]bool, len(known))
	for _, r := range known {
		name := r.RecordName()
		if name == "" {
			t.Errorf("%T has an empty RecordName", r)
		}
		if seen[name] {
			// Names are persisted and used to tell records apart in the audit
			// row, so a collision silently merges two different observations.
			t.Errorf("duplicate RecordName %q", name)
		}
		seen[name] = true
	}
}
