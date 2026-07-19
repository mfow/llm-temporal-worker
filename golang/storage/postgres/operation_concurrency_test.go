package postgres

import (
	"sync"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestOperationConcurrentBeginReplay(t *testing.T) {
	repository, ctx, cleanup := operationIntegrationRepository(t)
	defer cleanup()
	request := admission.BeginRequest{ID: "operation-concurrent-" + time.Now().UTC().Format("20060102150405.000000000"), ScopeKey: "concurrency/project", RequestDigest: admission.Digest([]byte("same")), ReservationUSD: pricing.MustUSD("0"), RequestManifest: []byte(`{}`)}
	const workers = 100
	results := make(chan admission.BeginResult, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			result, err := repository.Begin(ctx, request)
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	created := 0
	for result := range results {
		if !result.Existing {
			created++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d, want exactly one durable operation", created)
	}
}
