package budget

import (
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestReservationEventValidation(t *testing.T) {
	event := ReservationEvent{
		EventID: "event", GenerationID: "generation", OperationID: "operation", WindowID: "window",
		BucketStart: time.Unix(100, 0).UTC(), OccurredAt: time.Unix(101, 0).UTC(),
		AmountUSD: pricing.MustUSD("0.000000001"),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid reservation rejected: %v", err)
	}
	event.AmountUSD = pricing.MustUSD("0")
	if err := event.Validate(); err == nil {
		t.Fatal("zero reservation accepted")
	}
}

func TestCompletionEventEnforcesAccountingVocabulary(t *testing.T) {
	base := CompletionEvent{
		EventID: "event", GenerationID: "generation", OperationID: "operation", WindowID: "window",
		BucketStart: time.Unix(100, 0).UTC(), OccurredAt: time.Unix(101, 0).UTC(),
		Kind: JournalFinalizeUnknown, ReservedDecreaseUSD: pricing.MustUSD("1"),
		AccountedIncreaseUSD: pricing.MustUSD("1"), CostStatus: CostUnknown, UnknownReasonCode: "provider_unpriced",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid unknown finalization rejected: %v", err)
	}
	base.AccountedIncreaseUSD = pricing.MustUSD("0.9")
	if err := base.Validate(); err == nil {
		t.Fatal("inconsistent finalization accepted")
	}
	base.AccountedIncreaseUSD = pricing.MustUSD("1")
	base.UnknownReasonCode = "provider timeout"
	if err := base.Validate(); err == nil {
		t.Fatal("unsafe reason code accepted")
	}
}

func TestCompletionEventSupportsExactReleaseAndAmbiguousRetention(t *testing.T) {
	release := CompletionEvent{
		EventID: "event", GenerationID: "generation", OperationID: "operation", WindowID: "window",
		BucketStart: time.Unix(100, 0).UTC(), OccurredAt: time.Unix(101, 0).UTC(),
		Kind: JournalRelease, ReservedDecreaseUSD: pricing.MustUSD("1"),
		ActualCostUSD: ptrUSD(pricing.MustUSD("0")), CostStatus: CostExact,
	}
	if err := release.Validate(); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
	retain := release
	retain.Kind = JournalRetainAmbiguous
	retain.ReservedDecreaseUSD = pricing.MustUSD("0")
	retain.ActualCostUSD = nil
	retain.CostStatus = CostUnknown
	retain.UnknownReasonCode = "dispatch_ambiguous"
	if err := retain.Validate(); err != nil {
		t.Fatalf("valid retention rejected: %v", err)
	}
}

func ptrUSD(value pricing.USD) *pricing.USD { return &value }
