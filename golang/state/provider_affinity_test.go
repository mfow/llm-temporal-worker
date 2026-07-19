package state

import (
	"testing"
	"time"
)

func TestProviderCacheAffinityValidationKeepsExpiredHistoryAuditable(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	affinity := ProviderCacheAffinity{
		Rank: 0, Provider: "openai", RouteID: "route", EndpointID: "endpoint",
		EndpointAccountHMAC: [32]byte{1}, Region: "us-east-1", EndpointFamily: "responses",
		ModelLineage: "lineage", RouteModelRevision: "revision", CacheEpoch: "epoch",
		LastSuccessAt: now.Add(-2 * time.Minute), ExpiresAt: &expired,
	}
	if err := affinity.Validate(now); err != nil {
		t.Fatal(err)
	}
	if affinity.Active(now) {
		t.Fatal("expired affinity reported active")
	}
}

func TestProviderCacheAffinityValidationRequiresHMACAndSeparateUsage(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	base := ProviderCacheAffinity{
		Rank: 0, Provider: "openai", RouteID: "route", EndpointID: "endpoint",
		EndpointAccountHMAC: [32]byte{1}, Region: "us-east-1", EndpointFamily: "responses",
		ModelLineage: "lineage", RouteModelRevision: "revision", CacheEpoch: "epoch",
		LastSuccessAt: now,
	}
	withoutAccount := base
	withoutAccount.EndpointAccountHMAC = [32]byte{}
	if err := withoutAccount.Validate(now); err == nil {
		t.Fatal("affinity without endpoint-account HMAC accepted")
	}
	withoutKey := base
	withoutKey.HasProviderCacheKey = true
	if err := withoutKey.Validate(now); err == nil {
		t.Fatal("affinity with an empty provider cache HMAC accepted")
	}
	negativeWrite := base
	negativeWrite.ObservedCacheWriteTokens = -1
	if err := negativeWrite.Validate(now); err == nil {
		t.Fatal("negative cache-write tokens accepted")
	}
}
