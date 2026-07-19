package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

func operationIntegrationRepository(t *testing.T) (OperationRepository, context.Context, func()) {
	t.Helper()
	if os.Getenv("LLMTW_POSTGRES_ADDR") == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL operation tests")
	}
	ns, err := NewNamespace(valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"), valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"), os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(context.Background(), PoolOptions{Namespace: ns, Addresses: []string{os.Getenv("LLMTW_POSTGRES_ADDR")}, Username: valueOr("LLMTW_POSTGRES_USER", "llmtw"), Password: valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw"), MaxConnections: 8, MinConnections: 1, DialTimeout: 5 * time.Second, StatementTimeout: 5 * time.Second, LockTimeout: time.Second, IdleTxTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := Install(ctx, pool, ns); err != nil {
		cancel()
		pool.Close()
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	scopes := DefaultScopeRepository(pool, ns, ScopeKeyring{ActiveVersion: "scope-v1", Keys: map[string][]byte{"scope-v1": key}})
	repository := DefaultOperationRepository(pool, ns, Keyring{Active: "op-v1", Keys: map[string][]byte{"op-v1": key}}, scopes)
	return repository, ctx, func() { cancel(); pool.Close() }
}

func TestOperationReplayConflictAndResult(t *testing.T) {
	repository, ctx, cleanup := operationIntegrationRepository(t)
	defer cleanup()
	id := "operation-integration-" + time.Now().UTC().Format("20060102150405.000000000")
	request := admission.BeginRequest{ID: id, ScopeKey: "integration/project", RequestDigest: admission.Digest([]byte("request")), ReservationUSD: pricing.MustUSD("0"), ExpiresAt: time.Now().Add(time.Hour), RequestManifest: []byte(`{"model":"test"}`)}
	first, err := repository.Begin(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repository.Begin(ctx, request)
	if err != nil || !replay.Existing {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	request.RequestDigest = admission.Digest([]byte("different"))
	if _, err := repository.Begin(ctx, request); !errors.Is(err, admission.ErrOperationConflict) {
		t.Fatalf("conflict=%v", err)
	}
	request.RequestDigest = admission.Digest([]byte("request"))
	if err := repository.MarkDispatching(ctx, admission.DispatchRequest{OperationID: id, DispatchToken: first.Operation.DispatchToken, Attempt: admission.AttemptFacts{RouteID: "primary", EndpointID: "test", Provider: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkProviderPending(ctx, admission.ProviderPendingRequest{OperationID: id, DispatchToken: first.Operation.DispatchToken, ProviderOperationID: "provider-operation-1", EndpointID: "test", Provider: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkProviderPending(ctx, admission.ProviderPendingRequest{OperationID: id, DispatchToken: first.Operation.DispatchToken, ProviderOperationID: "provider-operation-2", EndpointID: "test", Provider: "fixture"}); !errors.Is(err, admission.ErrOperationConflict) {
		t.Fatalf("divergent provider operation = %v", err)
	}
	if providerID, err := repository.ProviderOperation(ctx, id); err != nil || providerID != "provider-operation-1" {
		t.Fatalf("provider operation reconciliation = %q, %v", providerID, err)
	}
	ref := &state.BlobRef{Digest: admission.Digest([]byte("result")), Size: 6, Media: "application/json"}
	if err := repository.Complete(ctx, admission.CompleteRequest{OperationID: id, DispatchToken: first.Operation.DispatchToken, ResultRef: ref, ActualCostUSD: pricing.MustUSD("0")}); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ScopeKey != request.ScopeKey || completed.ExpiresAt.IsZero() || completed.ResultRef == nil || *completed.ResultRef != *ref {
		t.Fatalf("hydrated operation metadata = %#v", completed)
	}
	attempts, err := repository.Attempts(ctx, id)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}
