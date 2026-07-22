package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestPricingCatalogRepositoryIntegration(t *testing.T) {
	addr := os.Getenv("LLMTW_POSTGRES_ADDR")
	if addr == "" {
		t.Skip("LLMTW_POSTGRES_ADDR is not configured; set it for PostgreSQL integration tests")
	}
	ns, err := NewNamespace(valueOr("LLMTW_POSTGRES_DATABASE", "llm_worker"), valueOr("LLMTW_POSTGRES_SCHEMA", "llm_worker"), os.Getenv("LLMTW_POSTGRES_TABLE_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	dsn := "postgres://" + valueOr("LLMTW_POSTGRES_USER", "llmtw") + ":" + valueOr("LLMTW_POSTGRES_PASSWORD", "llmtw") + "@" + addr + "/" + ns.Database + "?sslmode=disable"
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Install(ctx, pool, ns); err != nil {
		t.Fatal(err)
	}
	entry := pricing.Entry{
		Provider: "openai", Family: "openai_responses", EndpointID: "pricing-integration", Region: "global", Model: "gpt-test", ProviderTier: "standard",
		Prices:        pricing.UnitPrices{InputPerMillion: pricing.MustDecimalUSD("1.25"), OutputPerMillion: pricing.MustDecimalUSD("10.50"), CacheReadPerMillion: pricing.MustDecimalUSD("0"), CacheWritePerMillion: pricing.MustDecimalUSD("2"), ReasoningPerMillion: pricing.MustDecimalUSD("3"), PerRequest: pricing.MustDecimalUSD("0.10")},
		Version:       "pricing-integration-v1",
		EffectiveFrom: time.Unix(1_800_000_000, 0).UTC(),
	}
	partial := pricing.Entry{
		Provider: "openai", Family: "openai_responses", EndpointID: "pricing-integration", Region: "global", Model: "gpt-test", ProviderTier: "economy",
		Prices:            pricing.UnitPrices{InputPerMillion: pricing.MustDecimalUSD("1")},
		UnknownComponents: []pricing.PriceComponent{pricing.PriceComponentOutput, pricing.PriceComponentCacheRead, pricing.PriceComponentCacheWrite, pricing.PriceComponentReasoning, pricing.PriceComponentPerRequest},
		Version:           "pricing-integration-v1",
		EffectiveFrom:     time.Unix(1_800_000_000, 0).UTC(),
	}
	catalog, err := pricing.CompileUSD("pricing-integration-v1", []pricing.Entry{entry, partial})
	if err != nil {
		t.Fatal(err)
	}
	var sourceDigest [32]byte
	sourceDigest[0] = 7
	repository := PricingCatalogRepository{Pool: pool, Namespace: ns, NewID: func() uuid.UUID { return uuid.MustParse("8d0b6631-9c8c-4cf4-a5b2-8e53ce0f8792") }, Now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }}
	first, err := repository.Store(ctx, catalog, sourceDigest, time.Unix(1_800_000_100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Store(ctx, catalog, sourceDigest, first.EffectiveFrom)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(second.Catalog.Entries) != 2 {
		t.Fatalf("idempotent store = %#v, %#v", first, second)
	}
	loaded, err := repository.Load(ctx, catalog.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != first.ID || loaded.Catalog.Digest != first.Catalog.Digest || loaded.SourceDigest != sourceDigest || len(loaded.Catalog.Entries) != 2 {
		t.Fatalf("loaded snapshot = %#v, want id/digests and two entries", loaded)
	}
	var foundExact bool
	for _, loadedEntry := range loaded.Catalog.Entries {
		if loadedEntry.ProviderTier == "standard" {
			foundExact = true
			if loadedEntry.Version != catalog.Version {
				t.Fatalf("loaded price version = %q, want catalog version %q", loadedEntry.Version, catalog.Version)
			}
			if got := loadedEntry.Prices.OutputPerMillion.String(); got != "10.500000000000000000" {
				t.Fatalf("loaded output price = %q, want fixed-scale 10.500000000000000000", got)
			}
		}
	}
	if !foundExact {
		t.Fatal("loaded exact standard pricing entry not found")
	}
	active, err := repository.LoadActive(ctx, first.EffectiveFrom.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("active snapshot id = %s, want %s", active.ID, first.ID)
	}
	futureEntry := entry
	futureEntry.Version = "pricing-integration-v2"
	futureEntry.Prices.OutputPerMillion = pricing.MustDecimalUSD("11")
	futureCatalog, err := pricing.CompileUSD("pricing-integration-v2", []pricing.Entry{futureEntry})
	if err != nil {
		t.Fatal(err)
	}
	futureDigest := sourceDigest
	futureDigest[0] = 8
	futureRepository := repository
	futureRepository.NewID = func() uuid.UUID { return uuid.MustParse("7c9e7b77-bcc6-40ae-a74c-9891d94fbf6b") }
	futureAt := first.EffectiveFrom.Add(time.Hour)
	if _, err := futureRepository.Store(ctx, futureCatalog, futureDigest, futureAt); err != nil {
		t.Fatal(err)
	}
	beforeFuture, err := repository.LoadActive(ctx, first.EffectiveFrom.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if beforeFuture.ID != first.ID {
		t.Fatalf("pre-effective active snapshot id = %s, want predecessor %s", beforeFuture.ID, first.ID)
	}
	afterFuture, err := repository.LoadActive(ctx, futureAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if afterFuture.Catalog.Version != futureCatalog.Version {
		t.Fatalf("post-effective active catalog = %q, want %q", afterFuture.Catalog.Version, futureCatalog.Version)
	}
	entriesRelation, err := ns.Render("price_entries")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE "+entriesRelation+" SET source_price_digest=$1 WHERE price_catalog_id=$2", make([]byte, 32), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load(ctx, catalog.Version); err == nil {
		t.Fatal("mismatched persisted entry source digest unexpectedly loaded")
	}
}
