package redis

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestBudgetKeySpaceKeepsGenerationFamiliesInOneHashSlot(t *testing.T) {
	keys, err := NewBudgetKeySpace(KeyOptions{Prefix: "worker", HashTag: "budget", KeySecret: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatalf("NewBudgetKeySpace: %v", err)
	}
	for _, key := range []string{keys.ActiveGenerationKey(), keys.EventsKey(), keys.WorkersKey(), keys.ManifestKey("generation-1")} {
		if !strings.Contains(key, ":{budget}:") {
			t.Fatalf("key %q does not retain configured hash tag", key)
		}
	}
	if strings.Contains(keys.ManifestKey("tenant/raw"), "tenant/raw") {
		t.Fatal("manifest key exposed a raw generation identifier")
	}
}

func TestMemoryBudgetEventPortBroadcastsFromCursor(t *testing.T) {
	port := new(MemoryBudgetEventPort)
	base := BudgetStreamEvent{Schema: budgetStreamEventSchema, Kind: BudgetEventReserve, GenerationID: "generation-1", OperationHash: strings.Repeat("a", 64), MemberHash: strings.Repeat("b", 64), Revision: 1, NanoDelta: 4, OccurredAt: time.Unix(10, 0).UTC()}
	first, err := port.Append(context.Background(), base)
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	base.Kind = BudgetEventRelease
	second, err := port.Append(context.Background(), base)
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if first == second {
		t.Fatal("stream IDs are not monotonic")
	}
	rows, err := port.Read(context.Background(), first, 10)
	if err != nil {
		t.Fatalf("read after cursor: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != second || rows[0].Event.Kind != BudgetEventRelease {
		t.Fatalf("read after cursor = %#v", rows)
	}
}

func TestBudgetStreamEventRejectsRawOrUnboundedAccounting(t *testing.T) {
	event := BudgetStreamEvent{Schema: budgetStreamEventSchema, Kind: BudgetEventReserve, GenerationID: "generation", OperationHash: "operation", MemberHash: strings.Repeat("b", 64), OccurredAt: time.Unix(1, 0)}
	if err := event.Validate(); err == nil {
		t.Fatal("non-digest operation identifier accepted")
	}
	event.OperationHash = strings.Repeat("a", 64)
	event.NanoDelta = -1
	if err := event.Validate(); err == nil {
		t.Fatal("negative nano delta accepted")
	}
	event.NanoDelta = int64(pricing.NanoUSDSafeLimit) + 1
	if err := event.Validate(); err == nil {
		t.Fatal("unsafe nano delta accepted")
	}
}
