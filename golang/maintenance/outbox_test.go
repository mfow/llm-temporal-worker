package maintenance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testBlobEvent(t *testing.T, id string, at time.Time) Event {
	t.Helper()
	event, err := NewDeleteBlobEvent(id, "blob-"+id, at, at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestInMemoryOutboxDedupeAndBoundedConcurrentClaim(t *testing.T) {
	at := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	event := testBlobEvent(t, "event-1", at)
	store, err := NewInMemoryOutbox([]Event{event})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(context.Background(), event); err != nil {
		t.Fatalf("idempotent publish failed: %v", err)
	}
	conflict := event
	conflict.ID = "event-2"
	conflict.AggregateID = "different"
	if err := store.Publish(context.Background(), conflict); !errors.Is(err, ErrOutboxConflict) {
		t.Fatalf("expected dedupe conflict, got %v", err)
	}

	options := ClaimOptions{Now: at, Limit: 1, Lease: time.Minute}
	var wg sync.WaitGroup
	claimed := make(chan []Event, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, claimErr := store.Claim(context.Background(), options)
			if claimErr != nil {
				t.Errorf("claim failed: %v", claimErr)
				return
			}
			claimed <- items
		}()
	}
	wg.Wait()
	close(claimed)
	total := 0
	for items := range claimed {
		total += len(items)
	}
	if total != 1 {
		t.Fatalf("concurrent claims returned %d rows, want exactly one", total)
	}
	if err := store.Complete(context.Background(), event.ID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), event.ID, at.Add(time.Second)); !errors.Is(err, ErrOutboxNotClaimed) {
		t.Fatalf("completed row was mutable again: %v", err)
	}
}

func TestDispatcherMakesMissingObjectSuccessAndRetriesFailures(t *testing.T) {
	at := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	missing := testBlobEvent(t, "missing", at)
	failing := testBlobEvent(t, "failing", at)
	store, err := NewInMemoryOutbox([]Event{missing, failing})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := Dispatcher{Store: store, Delete: func(_ context.Context, event Event) error {
		if event.ID == missing.ID {
			return ErrObjectNotFound
		}
		return errors.New("object store unavailable")
	}}
	result, err := dispatcher.RunOnce(context.Background(), DispatchOptions{Now: at, Limit: 10, Lease: time.Minute, RetryDelay: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 2 || result.Completed != 1 || result.MissingObject != 1 || result.Retried != 1 {
		t.Fatalf("unexpected dispatch result: %+v", result)
	}
	for _, event := range store.Snapshot() {
		switch event.ID {
		case missing.ID:
			if event.State != EventCompleted {
				t.Errorf("missing object did not complete: %+v", event)
			}
		case failing.ID:
			if event.State != EventFailed || event.AttemptCount != 1 || !event.AvailableAt.Equal(at.Add(time.Minute)) {
				t.Errorf("failed object was not retryable: %+v", event)
			}
		}
	}
}

func TestOutboxLeaseRecovery(t *testing.T) {
	at := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	event := testBlobEvent(t, "lease", at)
	store, err := NewInMemoryOutbox([]Event{event})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), ClaimOptions{Now: at, Limit: 1, Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if items, err := store.Claim(context.Background(), ClaimOptions{Now: at.Add(30 * time.Second), Limit: 1, Lease: time.Minute}); err != nil {
		t.Fatal(err)
	} else if len(items) != 0 {
		t.Fatal("live lease was claimed twice")
	}
	items, err := store.Claim(context.Background(), ClaimOptions{Now: at.Add(2 * time.Minute), Limit: 1, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AttemptCount != 2 {
		t.Fatalf("expired lease was not recovered: %+v", items)
	}
}
