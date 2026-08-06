package fact_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// TestConstructorsProduceValidDeliveries is the floor: if the supported shapes
// did not survive their own validation, the rules would be describing something
// other than what the adapters are meant to report.
func TestConstructorsProduceValidDeliveries(t *testing.T) {
	cases := []struct {
		name string
		d    fact.Delivery
	}{
		{"whole response", fact.Succeeded(200)},
		{"stream died mid-flight", fact.Truncated(200, 200, fact.FaultUpstream, "stream_partial", errors.New("eof"))},
		{"caller left mid-stream", fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", errors.New("gone"))},
		{"caller left before any byte", fact.Undelivered(499, fact.VerdictSettled, fact.FaultClient, "client_disconnected", errors.New("gone"))},
		{"upstream failed, try next", fact.HandedOn(fact.FaultUpstream, "upstream_server_error", errors.New("boom"))},
		{"our own encoder failed", fact.HandedOn(fact.FaultGateway, "egress_rewrite_failed", errors.New("encode"))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestValidateRejectsImpossibleDeliveries builds one rule-breaker per rule.
//
// Each case describes a state the kernel must never act on. They are written as
// struct literals on purpose: the constructors cannot produce them, and the
// point is that a caller who bypasses the constructors is still caught.
func TestValidateRejectsImpossibleDeliveries(t *testing.T) {
	cases := []struct {
		name string
		d    fact.Delivery
		want string
	}{
		{
			name: "zero value claims nothing and everything",
			d:    fact.Delivery{},
			want: "verdict is unset",
		},
		{
			name: "no billing status leaves settlement with nothing to reason about",
			d:    fact.Delivery{Verdict: fact.VerdictSettled},
			want: "billing status is unset",
		},
		{
			name: "committed but wants another candidate",
			d: fact.Delivery{
				Committed: true, ClientStatus: 200, BillingStatus: 200,
				Verdict: fact.VerdictNextCandidate,
			},
			want: "no other candidate can serve this request",
		},
		{
			name: "committed but the caller saw no status",
			d: fact.Delivery{
				Committed: true, BillingStatus: 200, Verdict: fact.VerdictSettled,
			},
			want: "they saw a status",
		},
		{
			name: "uncommitted but claims the caller saw a status",
			d: fact.Delivery{
				ClientStatus: 200, BillingStatus: 502, Verdict: fact.VerdictNextCandidate,
			},
			want: "the caller saw no status at all",
		},
		{
			name: "complete without having sent anything",
			d: fact.Delivery{
				Complete: true, BillingStatus: 200, Verdict: fact.VerdictSettled,
			},
			want: "complete but nothing was committed",
		},
		{
			name: "complete yet settled as a failure",
			d: fact.Delivery{
				Committed: true, ClientStatus: 200, BillingStatus: 502,
				Complete: true, Verdict: fact.VerdictSettled,
			},
			want: "complete but billing status is 502",
		},
		{
			name: "complete yet somebody is to blame",
			d: fact.Delivery{
				Committed: true, ClientStatus: 200, BillingStatus: 200,
				Complete: true, Verdict: fact.VerdictSettled, Fault: fact.FaultUpstream,
			},
			want: "complete but blamed on upstream",
		},
		{
			// The shape five call sites used to produce: they sit in the 2xx
			// branch, the upstream's own status is the only number in reach,
			// and it means nothing here because this delivery is not the one
			// the caller is charged against.
			name: "handed on, yet carrying a number to bill",
			d: fact.Delivery{
				Committed: false, BillingStatus: 200,
				Verdict: fact.VerdictNextCandidate, Fault: fact.FaultUpstream,
			},
			want: "billing status 200 on a delivery that ends nothing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestTruncatedKeepsClientAndBillingStatusApart pins the reason this type
// exists. A caller who was served a 200 and then lost the connection saw a 200;
// settlement must still treat it as a 499. One number cannot say both, and the
// old code had to.
func TestTruncatedKeepsClientAndBillingStatusApart(t *testing.T) {
	d := fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", errors.New("gone"))

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if d.ClientStatus != 200 {
		t.Errorf("ClientStatus = %d, want 200: that is what the caller was served", d.ClientStatus)
	}
	if d.BillingStatus != 499 {
		t.Errorf("BillingStatus = %d, want 499: settlement must not see a success", d.BillingStatus)
	}
	if d.Complete {
		t.Error("Complete = true, want false: the caller did not receive the whole payload")
	}
}

// TestObservedCarriesTheAuditShape checks the audit record keeps the two
// statuses apart as well — a record that collapsed them would lose exactly the
// distinction it is being collected to measure.
func TestObservedCarriesTheAuditShape(t *testing.T) {
	got := fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", nil).Observed()

	if got.RecordName() != "delivery_observed" {
		t.Errorf("RecordName() = %q", got.RecordName())
	}
	if got.ClientStatus != 200 || got.BillingStatus != 499 {
		t.Errorf("Observed() = %+v, want the two statuses kept apart", got)
	}
	if got.Complete {
		t.Error("Complete = true, want false")
	}
	if got.Fault != "client" {
		t.Errorf("Fault = %q, want %q", got.Fault, "client")
	}

	// It must satisfy Record, or it cannot reach the audit trail at all.
	var _ fact.Record = got
}
