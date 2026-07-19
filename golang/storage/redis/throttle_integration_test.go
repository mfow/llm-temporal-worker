//go:build integration

package redis

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLiveRedisThrottleAcquireReplayDenialAndRelease(t *testing.T) {
	client := openLiveRedis(t)
	keys := liveKeyOptions("throttle")
	cleanupLivePrefix(t, client, keys.Prefix)
	store, err := NewThrottleStore(ThrottleOptions{
		Client: client,
		Mode:   AdmissionModeFunction,
		Keys:   keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := []ThrottleLimit{
		{Kind: ThrottleRequests, Scope: "tenant-a", Amount: 2, Limit: 2, Window: time.Minute},
		{Kind: ThrottleTokens, Scope: "tenant-a", Amount: 4, Limit: 8, Window: time.Minute},
	}
	first, err := store.Acquire(context.Background(), "live-throttle-1", limits)
	if err != nil || first.Existing {
		t.Fatalf("live throttle acquire = %#v, %v", first, err)
	}
	replay, err := store.Acquire(context.Background(), "live-throttle-1", limits)
	if err != nil || !replay.Existing {
		t.Fatalf("live throttle replay = %#v, %v", replay, err)
	}
	if _, err := store.Acquire(context.Background(), "live-throttle-2", limits); !errors.Is(err, ErrThrottleDenied) {
		t.Fatalf("live throttle denial = %v", err)
	}
	if err := store.Release(context.Background(), first.Reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(context.Background(), "live-throttle-3", limits); err != nil {
		t.Fatalf("live throttle after release = %v", err)
	}
}
