package postgres

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"
)

func TestPostgresFoundationIntegration(t *testing.T) {
	addr := os.Getenv("LLMTW_POSTGRES_ADDR")
	if addr == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL integration tests")
	}
	namespace, err := NewNamespace(valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"), valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"), os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(context.Background(), PoolOptions{
		Namespace:        namespace,
		Addresses:        []string{addr},
		Username:         valueOr("LLMTW_POSTGRES_USER", "llmtw"),
		Password:         valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw"),
		MaxConnections:   4,
		MinConnections:   1,
		DialTimeout:      5 * time.Second,
		StatementTimeout: 5 * time.Second,
		LockTimeout:      time.Second,
		IdleTxTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Install(ctx, pool, namespace); err != nil {
		t.Fatalf("install schema: %v", err)
	}
	if err := Health(ctx, pool, namespace); err != nil {
		t.Fatalf("health: %v", err)
	}
	keys := ScopeKeyring{ActiveVersion: "scope-v1", Keys: map[string][]byte{"scope-v1": []byte("01234567890123456789012345678901")}}
	scope := DefaultScopeRepository(pool, namespace, keys)
	first, err := scope.Ensure(ctx, "tenant-foundation", "project-foundation")
	if err != nil {
		t.Fatalf("ensure scope: %v", err)
	}
	second, err := scope.Ensure(ctx, "tenant-foundation", "project-foundation")
	if err != nil {
		t.Fatalf("ensure scope replay: %v", err)
	}
	if first.ID != second.ID || first.TenantHMAC != second.TenantHMAC || first.ProjectHMAC != second.ProjectHMAC {
		t.Fatalf("scope replay changed identity: first=%#v second=%#v", first, second)
	}
	blob := BlobRepository{
		Pool:      pool,
		Namespace: namespace,
		Keys: Keyring{Active: "blob-v1", Keys: map[string][]byte{
			"blob-v1": []byte("abcdefghijklmnopqrstuvwxyz123456")[:32],
		}},
		NewID: UUIDv7,
	}
	value := []byte("encrypted foundation locator")
	record, err := blob.PutLocator(ctx, first.ID, "request", BlobMetadata{ScopeID: first.ID, StoreID: "test-store", Digest: sha256.Sum256([]byte("object bytes")), ByteLength: int64(len("object bytes")), MediaType: "application/octet-stream"}, value)
	if err != nil {
		t.Fatalf("put locator: %v", err)
	}
	opened, err := blob.OpenLocator(ctx, first.ID, "request", record)
	if err != nil || string(opened) != string(value) {
		t.Fatalf("open locator = %q, %v", opened, err)
	}
	reused, err := blob.PutLocator(ctx, first.ID, "request", BlobMetadata{ScopeID: first.ID, StoreID: "test-store", Digest: sha256.Sum256([]byte("object bytes")), ByteLength: int64(len("object bytes")), MediaType: "application/octet-stream"}, []byte("a shorter locator"))
	if err != nil {
		t.Fatalf("reuse locator row: %v", err)
	}
	if reused.BlobID != record.BlobID {
		t.Fatalf("content-addressed locator reuse changed blob id: %s != %s", reused.BlobID, record.BlobID)
	}
	if opened, err := blob.OpenLocator(ctx, first.ID, "request", reused); err != nil || string(opened) != string(value) {
		t.Fatalf("open reused locator = %q, %v", opened, err)
	}
	loaded, err := blob.Get(ctx, first.ID, record.BlobID)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	if loaded.BlobID != record.BlobID || loaded.ScopeID != first.ID || loaded.ByteLength != int64(len("object bytes")) {
		t.Fatalf("loaded blob metadata = %#v", loaded)
	}
}
