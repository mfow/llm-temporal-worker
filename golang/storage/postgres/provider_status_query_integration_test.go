package postgres

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
)

func TestProviderStatusProjectionQueryIntegration(t *testing.T) {
	ctx, namespace, pool, cleanup := providerControlIntegrationPool(t)
	defer cleanup()

	configDigest := sha256.Sum256([]byte("provider-status-query-" + time.Now().UTC().Format(time.RFC3339Nano)))
	configs, err := namespace.Render("configuration_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+configs+" (config_digest, config_version, source_digest, sanitized_config) VALUES ($1,$2,$1,'{}'::jsonb)", configDigest[:], "provider-status-query"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	observations := []control.StatusObservation{
		{
			ConfigDigest: configDigest, RouteID: "route-a", EndpointID: "endpoint-a",
			EndpointAccountHMAC: sha256.Sum256([]byte("account-a")), Provider: "provider-a", EndpointFamily: "chat",
			ObservedAt: now, Source: control.SourceInference, Availability: control.AvailabilityAvailable,
			Credit: control.CreditOK, Billing: control.BillingOK, EvidenceDigest: sha256.Sum256([]byte("evidence-a")),
			ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-b", EndpointID: "endpoint-b",
			EndpointAccountHMAC: sha256.Sum256([]byte("account-b")), Provider: "provider-b", EndpointFamily: "chat",
			ObservedAt: now.Add(time.Second), Source: control.SourceInference, Availability: control.AvailabilityDegraded,
			Credit: control.CreditOK, Billing: control.BillingOK, EvidenceDigest: sha256.Sum256([]byte("evidence-b")),
			ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
		{
			ConfigDigest: configDigest, RouteID: "route-c", EndpointID: "endpoint-c",
			EndpointAccountHMAC: sha256.Sum256([]byte("account-c")), Provider: "provider-a", EndpointFamily: "chat",
			ObservedAt: now.Add(2 * time.Second), Source: control.SourceInference, Availability: control.AvailabilityUnavailable,
			Credit: control.CreditUnknown, Billing: control.BillingUnknown, EvidenceDigest: sha256.Sum256([]byte("evidence-c")),
			ConfigEpoch: "epoch-1", ExpiresAt: now.Add(time.Hour),
		},
	}
	repository := DefaultProviderStatusRepository(pool, namespace)
	for _, observation := range observations {
		event, err := control.NewStatusEvent(observation)
		if err != nil {
			t.Fatal(err)
		}
		if applied, err := repository.PersistStatusEvent(ctx, event); err != nil || !applied {
			t.Fatalf("persist %s: applied=%v err=%v", observation.RouteID, applied, err)
		}
	}

	page, err := repository.ListRouteStatuses(ctx, ProviderStatusListOptions{ConfigDigest: configDigest, IncludeHealthy: false, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routes) != 1 || page.Routes[0].RouteID != "route-b" || page.NextRouteID != "route-b" {
		t.Fatalf("unhealthy page = %#v, next=%q", page.Routes, page.NextRouteID)
	}

	routes, err := namespace.Render("provider_route_status")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE "+routes+" SET circuit_state='open' WHERE config_digest=$1 AND route_id='route-a'", configDigest[:]); err != nil {
		t.Fatal(err)
	}
	page, err = repository.ListRouteStatuses(ctx, ProviderStatusListOptions{ConfigDigest: configDigest, IncludeHealthy: false, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routes) != 2 || page.Routes[0].RouteID != "route-a" || page.Routes[1].RouteID != "route-b" || page.NextRouteID != "route-b" {
		t.Fatalf("open-circuit unhealthy page = %#v, next=%q", page.Routes, page.NextRouteID)
	}

	page, err = repository.ListRouteStatuses(ctx, ProviderStatusListOptions{ConfigDigest: configDigest, IncludeHealthy: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routes) != 2 || page.Routes[0].RouteID != "route-a" || page.Routes[1].RouteID != "route-b" || page.NextRouteID != "route-b" {
		t.Fatalf("first all-routes page = %#v, next=%q", page.Routes, page.NextRouteID)
	}

	page, err = repository.ListRouteStatuses(ctx, ProviderStatusListOptions{ConfigDigest: configDigest, Provider: "provider-a", IncludeHealthy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routes) != 2 || page.Routes[0].RouteID != "route-a" || page.Routes[1].RouteID != "route-c" {
		t.Fatalf("provider filter = %#v", page.Routes)
	}

	page, err = repository.ListRouteStatuses(ctx, ProviderStatusListOptions{ConfigDigest: configDigest, IncludeHealthy: true, AfterRouteID: "route-b", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Routes) != 1 || page.Routes[0].RouteID != "route-c" || page.NextRouteID != "" {
		t.Fatalf("continuation page = %#v, next=%q", page.Routes, page.NextRouteID)
	}
}
