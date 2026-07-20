package postgres

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
)

func TestCreditStatusProjectionQueryIntegration(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	defer cleanup()

	configDigest := sha256.Sum256([]byte("credit-status-query-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "credit-status-query"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	observations := []control.StatusObservation{
		{
			ConfigDigest: configDigest, RouteID: "route-credit", EndpointID: "endpoint-credit",
			EndpointAccountHMAC: sha256.Sum256([]byte("credit-account")), Provider: "provider-credit", EndpointFamily: "chat",
			ObservedAt: now, Source: control.SourceManagementAPI, Availability: control.AvailabilityUnavailable,
			Credit: control.CreditExhausted, Billing: control.BillingIssue, ProviderCode: "insufficient_quota",
			EvidenceDigest: sha256.Sum256([]byte("credit-evidence")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-credit", EndpointID: "endpoint-credit",
			EndpointAccountHMAC: sha256.Sum256([]byte("credit-account")), Provider: "provider-credit", EndpointFamily: "chat",
			ObservedAt: now.Add(250 * time.Millisecond), Source: control.SourceInference, Availability: control.AvailabilityAvailable,
			Credit: control.CreditOK, Billing: control.BillingOK,
			EvidenceDigest: sha256.Sum256([]byte("credit-inference-ok")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-healthy", EndpointID: "endpoint-healthy",
			EndpointAccountHMAC: sha256.Sum256([]byte("healthy-account")), Provider: "provider-healthy", EndpointFamily: "chat",
			ObservedAt: now.Add(time.Second), Source: control.SourceManagementAPI, Availability: control.AvailabilityAvailable,
			Credit: control.CreditOK, Billing: control.BillingOK,
			EvidenceDigest: sha256.Sum256([]byte("healthy-evidence")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-credit-latest", EndpointID: "endpoint-credit",
			EndpointAccountHMAC: sha256.Sum256([]byte("credit-account")), Provider: "provider-credit", EndpointFamily: "chat",
			ObservedAt: now.Add(500 * time.Millisecond), Source: control.SourceManagementAPI, Availability: control.AvailabilityUnavailable,
			Credit: control.CreditExhausted, Billing: control.BillingIssue, ProviderCode: "billing_hard_limit",
			EvidenceDigest: sha256.Sum256([]byte("credit-latest-evidence")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-low", EndpointID: "endpoint-low",
			EndpointAccountHMAC: sha256.Sum256([]byte("low-account")), Provider: "provider-low", EndpointFamily: "chat",
			ObservedAt: now.Add(2 * time.Second), Source: control.SourceOperator, Availability: control.AvailabilityDegraded,
			Credit: control.CreditLow, Billing: control.BillingUnknown, SafeErrorCode: "credit_low",
			EvidenceDigest: sha256.Sum256([]byte("low-evidence")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-sticky", EndpointID: "endpoint-sticky",
			EndpointAccountHMAC: sha256.Sum256([]byte("sticky-account")), Provider: "provider-sticky", EndpointFamily: "chat",
			ObservedAt: now.Add(3 * time.Second), Source: control.SourceManagementAPI, Availability: control.AvailabilityUnavailable,
			Credit: control.CreditExhausted, Billing: control.BillingIssue, ProviderCode: "insufficient_quota",
			EvidenceDigest: sha256.Sum256([]byte("sticky-evidence")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-sticky", EndpointID: "endpoint-sticky",
			EndpointAccountHMAC: sha256.Sum256([]byte("sticky-account")), Provider: "provider-sticky", EndpointFamily: "chat",
			ObservedAt: now.Add(4 * time.Second), Source: control.SourceInference, Availability: control.AvailabilityAvailable,
			Credit: control.CreditOK, Billing: control.BillingOK,
			EvidenceDigest: sha256.Sum256([]byte("sticky-inference-ok")), ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
	}
	repository := DefaultProviderStatusRepository(pool, namespace)
	for _, observation := range observations {
		event, err := control.NewStatusEvent(observation)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := repository.PersistStatusEvent(ctx, event); err != nil || !applied {
			t.Fatalf("persist %s: applied=%v err=%v", observation.EndpointID, applied, err)
		}
	}

	page, err := repository.ListCreditStatuses(ctx, CreditStatusListOptions{ConfigDigest: configDigest, IncludeOK: false, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Endpoints) != 1 || page.Endpoints[0].EndpointID != "endpoint-credit" || page.NextEndpointKey != "provider-credit\x00endpoint-credit" {
		t.Fatalf("incident page = %#v, next=%q", page.Endpoints, page.NextEndpointKey)
	}
	if page.Endpoints[0].EvidenceSource != control.CreditEvidenceProviderAPI || page.Endpoints[0].SafeEvidenceCode != "billing_hard_limit" {
		t.Fatalf("provider evidence = %#v", page.Endpoints[0])
	}
	sticky, err := repository.ListCreditStatuses(ctx, CreditStatusListOptions{ConfigDigest: configDigest, Provider: "provider-sticky", EndpointID: "endpoint-sticky", IncludeOK: false, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(sticky.Endpoints) != 1 || sticky.Endpoints[0].EvidenceSource != control.CreditEvidenceProviderAPI || sticky.Endpoints[0].SafeEvidenceCode != "insufficient_quota" {
		t.Fatalf("sticky provider evidence = %#v", sticky.Endpoints)
	}

	page, err = repository.ListCreditStatuses(ctx, CreditStatusListOptions{ConfigDigest: configDigest, IncludeOK: false, AfterEndpointKey: page.NextEndpointKey, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Endpoints) != 2 || page.Endpoints[0].EndpointID != "endpoint-low" || page.Endpoints[1].EndpointID != "endpoint-sticky" || page.NextEndpointKey != "" {
		t.Fatalf("continuation page = %#v, next=%q", page.Endpoints, page.NextEndpointKey)
	}
	if page.Endpoints[0].EvidenceSource != control.CreditEvidenceOperator || page.Endpoints[0].SafeEvidenceCode != "credit_low" {
		t.Fatalf("operator evidence = %#v", page.Endpoints[0])
	}

	page, err = repository.ListCreditStatuses(ctx, CreditStatusListOptions{ConfigDigest: configDigest, IncludeOK: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Endpoints) != 4 || page.Endpoints[1].EndpointID != "endpoint-healthy" {
		t.Fatalf("all endpoint page = %#v", page.Endpoints)
	}
}
