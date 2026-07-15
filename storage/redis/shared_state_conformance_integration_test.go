//go:build integration

package redis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/state"
	"github.com/mfow/llm-temporal-worker/storage/conformance"
	redisclient "github.com/redis/go-redis/v9"
)

var liveConformanceSequence atomic.Uint64

func TestLiveRedisStoreFactoryConformance(t *testing.T) {
	client := openLiveRedis(t)
	conformance.Run(t, liveRedisStoreFactory(client))
}

func TestLiveRedisTimeoutAfterMutationResolvesByRead(t *testing.T) {
	client := openLiveRedis(t)
	now := time.Now().UTC()
	keys := liveKeyOptions("timeout")
	cleanupLivePrefix(t, client, keys.Prefix)
	invoker := &timeoutAfterMutationInvoker{
		inner: redisInvoker{client: client, mode: AdmissionModeFunction, version: AdmissionFunctionVersion},
	}
	store, err := NewAdmissionStore(AdmissionOptions{
		Client:  client,
		Reader:  redisReader{client: client},
		Invoker: invoker,
		Keys:    keys,
		Clock:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := liveBeginRequest("timeout-after-mutation", "timeout-policy", 1, 10, now)
	if _, err := store.Begin(context.Background(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout after mutation = %v, want unresolved shared state", err)
	}
	resolved, err := store.Get(context.Background(), request.ID)
	if err != nil || resolved.State != admission.StateReserved || resolved.ReservedMicroUSD != 1 {
		t.Fatalf("read resolution = %#v, %v", resolved, err)
	}
}

func TestLiveRedisFunctionAndLuaMismatchFailClosed(t *testing.T) {
	client := openLiveRedis(t)
	now := time.Now().UTC()
	keys := liveKeyOptions("mismatch")
	cleanupLivePrefix(t, client, keys.Prefix)
	request := liveBeginRequest("mismatch", "mismatch-policy", 1, 10, now)

	functionStore, err := NewAdmissionStore(AdmissionOptions{
		Client:          client,
		Mode:            AdmissionModeFunction,
		FunctionVersion: "admission_v999",
		Keys:            keys,
		Clock:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := functionStore.Begin(context.Background(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("function version mismatch = %v, want unresolved shared state", err)
	}

	luaStore, err := NewAdmissionStore(AdmissionOptions{
		Client: client,
		Mode:   AdmissionModeLua,
		Keys:   keys,
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := luaStore.Begin(context.Background(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Lua digest mismatch = %v, want unresolved shared state", err)
	}

	space, err := newKeySpace(keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		space.scopeKey(request.ScopeKey),
		space.operationIndexKey(request.ID),
		space.operationKey(request.ScopeKey, request.ID),
		space.budgetKey("mismatch-policy", "hour"),
		space.continuationIndexKey("opaque-handle"),
		space.continuationKey("tenant-a", "opaque-handle"),
	} {
		if !strings.HasPrefix(key, keys.Prefix+":{"+keys.HashTag+"}") {
			t.Fatalf("live mutation key escapes configured hash slot: %q", key)
		}
	}
}

func TestLiveRedisConfiguredPersistenceSurvivesRestart(t *testing.T) {
	container := os.Getenv("LLMTW_REDIS_CONTAINER")
	if container == "" {
		t.Skip("make redis-integration supplies the isolated Redis container")
	}
	if os.Getenv("LLMTW_REDIS_TEST_PROVISION") != "1" {
		t.Skip("restart is restricted to make redis-integration's explicitly provisioned dependency")
	}
	configuredPrefix := os.Getenv("LLMTW_REDIS_CONTAINER_PREFIX")
	if !isLiveRedisPersistenceContainer(container, configuredPrefix) {
		t.Skip("restart is restricted to make redis-integration's configured local dependency")
	}
	client := openLiveRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settings, err := client.ConfigGet(ctx, "appendonly").Result()
	if err != nil || settings["appendonly"] != "yes" {
		t.Fatalf("Redis appendonly configuration is not enabled")
	}
	settings, err = client.ConfigGet(ctx, "appendfsync").Result()
	if err != nil || settings["appendfsync"] != "always" {
		t.Fatalf("Redis appendfsync configuration is not durable")
	}
	settings, err = client.ConfigGet(ctx, "save").Result()
	if err != nil || !strings.Contains(settings["save"], "60 1") {
		t.Fatalf("Redis snapshot persistence configuration is not present")
	}

	now := time.Now().UTC()
	keys := liveKeyOptions("restart")
	store, err := NewAdmissionStore(AdmissionOptions{
		Client: client,
		Mode:   AdmissionModeFunction,
		Keys:   keys,
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := liveBeginRequest("restart", "restart-policy", 1, 10, now)
	if result, err := store.Begin(ctx, request); err != nil || result.Denied != nil {
		t.Fatalf("write before Redis restart = %#v, %v", result, err)
	}
	runLiveRedisDocker(t, "restart", container)
	restartedClient := reopenLiveRedisAfterRestart(t, container)
	cleanupLivePrefix(t, restartedClient, keys.Prefix)
	restartedStore, err := NewAdmissionStore(AdmissionOptions{
		Client: restartedClient,
		Mode:   AdmissionModeFunction,
		Keys:   keys,
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := restartedStore.Get(context.Background(), request.ID)
	if err != nil || stored.State != admission.StateReserved {
		t.Fatalf("persisted operation after Redis restart = %#v, %v", stored, err)
	}
	if result, err := restartedStore.Begin(context.Background(), liveBeginRequest("restart-after", "restart-policy", 1, 10, now)); err != nil || result.Denied != nil {
		t.Fatalf("Function after Redis restart = %#v, %v", result, err)
	}
}

func TestLiveRedisConfiguredPersistenceGateAllowsOverriddenPrefix(t *testing.T) {
	const prefix = "llmtw-redis-custom-ci"
	if !isLiveRedisPersistenceContainer(prefix+"-123-456", prefix) {
		t.Fatal("configured Redis container prefix should allow the persistence gate to run")
	}
}

func TestLiveRedisAddressForPublishedPort(t *testing.T) {
	address, err := liveRedisAddressForPublishedPort("32768")
	if err != nil {
		t.Fatal(err)
	}
	if want := "127.0.0.1:32768"; address != want {
		t.Fatalf("address = %q, want %q", address, want)
	}
	for _, port := range []string{"", "not-a-port", "0", "65536"} {
		if _, err := liveRedisAddressForPublishedPort(port); err == nil {
			t.Errorf("port %q was accepted", port)
		}
	}
}

func isLiveRedisPersistenceContainer(container, configuredPrefix string) bool {
	return configuredPrefix != "" && strings.HasPrefix(container, configuredPrefix+"-")
}

func openLiveRedis(t *testing.T) *redisclient.Client {
	t.Helper()
	address := os.Getenv("LLMTW_REDIS_ADDR")
	if address == "" {
		t.Skip("set LLMTW_REDIS_ADDR to run the pinned live Redis gate")
	}
	client := openLiveRedisAt(t, address)
	if os.Getenv("LLMTW_REDIS_TEST_PROVISION") == "1" {
		if err := client.FunctionLoad(context.Background(), AdmissionFunctionSource()).Err(); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatal("could not provision the isolated Redis Function")
		}
	}
	return client
}

func openLiveRedisAt(t *testing.T, address string) *redisclient.Client {
	t.Helper()
	client := redisclient.NewClient(&redisclient.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	waitForLiveRedis(t, client)
	return client
}

func reopenLiveRedisAfterRestart(t *testing.T, container string) *redisclient.Client {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		address, err := liveRedisAddressForContainer(container)
		if err == nil {
			client := redisclient.NewClient(&redisclient.Options{Addr: address})
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			err = client.Ping(ctx).Err()
			cancel()
			if err == nil {
				t.Cleanup(func() { _ = client.Close() })
				return client
			}
			_ = client.Close()
			lastErr = fmt.Errorf("ping Redis at %s: %w", address, err)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("isolated Redis did not become reachable after restart: %v", lastErr)
	return nil
}

func liveRedisAddressForContainer(container string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{if .State.Running}}{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostPort}}{{end}}`, container).Output()
	if err != nil {
		return "", fmt.Errorf("inspect restarted Redis container: %w", err)
	}
	return liveRedisAddressForPublishedPort(strings.TrimSpace(string(output)))
}

func liveRedisAddressForPublishedPort(port string) (string, error) {
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", fmt.Errorf("invalid Redis published port %q", port)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

func waitForLiveRedis(t *testing.T, client *redisclient.Client) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := client.Ping(ctx).Err()
		cancel()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("isolated Redis did not become ready")
}

func liveRedisStoreFactory(client *redisclient.Client) conformance.StoreFactory {
	return conformance.StoreFactory{
		Name: "redis",
		New: func(t testing.TB) conformance.Stores {
			t.Helper()
			now := time.Now().UTC()
			keys := liveKeyOptions("conformance")
			cleanupLivePrefix(t, client, keys.Prefix)
			keyring, err := state.NewKeyring([]state.Key{{
				ID:      "conformance",
				Secret:  bytes.Repeat([]byte{2}, 32),
				Primary: true,
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			admissions, err := NewAdmissionStore(AdmissionOptions{
				Client: client,
				Mode:   AdmissionModeFunction,
				Keys:   keys,
				Clock:  func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			continuations, err := NewContinuationStore(ContinuationOptions{
				Client:  client,
				Keys:    keys,
				Keyring: keyring,
				Clock:   func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			return conformance.Stores{
				Admission:     admissions,
				Continuations: continuations,
				Now:           func() time.Time { return now },
			}
		},
	}
}

func liveKeyOptions(kind string) KeyOptions {
	sequence := liveConformanceSequence.Add(1)
	return KeyOptions{
		Prefix:    fmt.Sprintf("live-%s-%d-%d", kind, time.Now().UnixNano(), sequence),
		HashTag:   "admission",
		KeySecret: bytes.Repeat([]byte{3}, 32),
	}
}

func liveBeginRequest(id, policy string, amount, limit int, now time.Time) admission.BeginRequest {
	return admission.BeginRequest{
		ID:            id,
		ScopeKey:      "tenant/" + id,
		RequestDigest: admission.Digest([]byte("request/" + id)),
		Reservation:   pricing.MicroUSD(amount),
		Reservations: []admission.WindowReservation{{
			PolicyID:      policy,
			WindowID:      "hour",
			Bucket:        now.UnixNano() / int64(time.Hour),
			Amount:        pricing.MicroUSD(amount),
			Limit:         pricing.MicroUSD(limit),
			BucketNanos:   int64(time.Hour),
			DurationNanos: int64(2 * time.Hour),
		}},
		LeaseUntil: now.Add(time.Minute),
		ExpiresAt:  now.Add(2 * time.Hour),
	}
}

func cleanupLivePrefix(t testing.TB, client *redisclient.Client, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, prefix+":*", 100).Result()
			if err != nil {
				t.Error("could not scan isolated Redis test keys during cleanup")
				return
			}
			if len(keys) != 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Error("could not remove isolated Redis test keys during cleanup")
					return
				}
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})
}

func runLiveRedisDocker(t *testing.T, arguments ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", arguments...).Run(); err != nil {
		t.Fatal("could not restart the isolated Redis dependency")
	}
}

type timeoutAfterMutationInvoker struct {
	inner FunctionInvoker
	once  atomic.Bool
}

func (invoker *timeoutAfterMutationInvoker) Run(ctx context.Context, name string, keys []string, args ...string) ([]any, error) {
	result, err := invoker.inner.Run(ctx, name, keys, args...)
	if err != nil {
		return nil, err
	}
	if invoker.once.CompareAndSwap(false, true) {
		return nil, context.DeadlineExceeded
	}
	return result, nil
}
