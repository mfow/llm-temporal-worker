package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/budget"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestBudgetJournalRejectsInvalidEventsBeforePoolAccess(t *testing.T) {
	namespace, err := NewNamespace("worker", "llm_worker", "")
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	repository := &BudgetJournalRepository{Namespace: namespace}
	event := budget.ReservationEvent{EventID: "not-a-uuid", GenerationID: "generation", OperationID: "operation", WindowID: "window", BucketStart: time.Unix(1, 0), OccurredAt: time.Unix(2, 0), AmountUSD: pricing.MustUSD("1")}
	if _, err := repository.AppendReservation(context.Background(), event); err == nil || !strings.Contains(err.Error(), "budget journal PostgreSQL pool") {
		t.Fatalf("nil pool validation = %v", err)
	}
	repository.Pool = nil
	if _, err := repository.AppendReservation(context.Background(), event); err == nil {
		t.Fatal("invalid repository unexpectedly accepted")
	}
}

func TestBudgetJournalCompletionProjectionMatchesSchemaStates(t *testing.T) {
	for _, test := range []struct {
		kind  budget.JournalEventKind
		state string
		basis string
	}{
		{budget.JournalRetainAmbiguous, "retained_ambiguous", "retained_bound"},
		{budget.JournalFinalizeUnknown, "finalized", "retained_bound"},
		{budget.JournalRelease, "released", "released"},
		{budget.JournalFinalizeExact, "finalized", "exact_actual"},
		{budget.JournalResolveUnknownExact, "finalized", "exact_actual"},
	} {
		state, basis, _ := completionProjection(journalInput{kind: test.kind, occurredAt: time.Unix(1, 0)})
		if state != test.state || basis != test.basis {
			t.Fatalf("projection(%s) = %q/%q, want %q/%q", test.kind, state, basis, test.state, test.basis)
		}
	}
}

func TestBudgetJournalOnlyRendersValidatedRelations(t *testing.T) {
	namespace, err := NewNamespace("worker", "llm_worker", "tenant_")
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	for _, logical := range []string{"budget_journal_events", "budget_buckets", "operation_budget_reservations"} {
		relation, err := namespace.Render(logical)
		if err != nil {
			t.Fatalf("render %s: %v", logical, err)
		}
		if !strings.Contains(relation, `"llm_worker"`) || !strings.Contains(relation, `"tenant_`) {
			t.Fatalf("relation %q is not schema/prefix qualified", relation)
		}
	}
}
