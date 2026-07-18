package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/config"
	redisstore "github.com/mfow/llm-temporal-worker/golang/storage/redis"
	redisclient "github.com/redis/go-redis/v9"
)

// DependencyID is a stable, non-secret identity used only inside the runtime
// readiness gate. HTTP probes intentionally expose none of these details.
type DependencyID string

const (
	DependencyRedis     DependencyID = "redis"
	DependencyBlobStore DependencyID = "blob_store"
)

// ProbeStatus and ProbeReason are closed sets so a dependency implementation
// cannot accidentally surface endpoint URLs, credentials, or SDK error text.
type ProbeStatus string

const (
	ProbeStatusReady       ProbeStatus = "ready"
	ProbeStatusUnavailable ProbeStatus = "unavailable"
	ProbeStatusPolicy      ProbeStatus = "policy_mismatch"
	ProbeStatusTimeout     ProbeStatus = "timeout"
)

type ProbeReason string

const (
	ProbeReasonReady          ProbeReason = "ready"
	ProbeReasonUnavailable    ProbeReason = "unavailable"
	ProbeReasonPolicyMismatch ProbeReason = "policy_mismatch"
	ProbeReasonTimeout        ProbeReason = "timeout"
)

// ProbeResult carries only a stable dependency identity and a bounded,
// operator-safe result. The HTTP layer receives only HealthState, never this
// result or the underlying dependency client.
type ProbeResult struct {
	Dependency DependencyID
	Status     ProbeStatus
	Reason     ProbeReason
}

// DependencyProbe is the narrow runtime injection seam for required external
// state. Implementations must honor the supplied context and return one of the
// safe result combinations defined above.
type DependencyProbe interface {
	Probe(context.Context) ProbeResult
}

type DependencyProbeFunc func(context.Context) ProbeResult

func (function DependencyProbeFunc) Probe(ctx context.Context) ProbeResult { return function(ctx) }

// CheckDependencyProbes runs each required dependency with an individual,
// bounded context. It deliberately returns a generic error: the status is
// available to internal callers, but raw SDK errors never cross the runtime
// readiness boundary.
func CheckDependencyProbes(ctx context.Context, probes []DependencyProbe, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return errRequiredDependencyUnavailable
	}
	for _, probe := range probes {
		if probe == nil {
			return errRequiredDependencyUnavailable
		}
		probeContext, cancel := context.WithTimeout(ctx, timeout)
		result := normalizeProbeResult(probe.Probe(probeContext))
		cancel()
		if result.Status != ProbeStatusReady {
			return errRequiredDependencyUnavailable
		}
	}
	return nil
}

var errRequiredDependencyUnavailable = errors.New("required runtime dependency is unavailable")

func normalizeProbeResult(result ProbeResult) ProbeResult {
	if result.Dependency != DependencyRedis && result.Dependency != DependencyBlobStore {
		return ProbeResult{Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
	}
	switch result.Status {
	case ProbeStatusReady:
		if result.Reason == ProbeReasonReady {
			return result
		}
	case ProbeStatusUnavailable:
		if result.Reason == ProbeReasonUnavailable {
			return result
		}
	case ProbeStatusPolicy:
		if result.Reason == ProbeReasonPolicyMismatch {
			return result
		}
	case ProbeStatusTimeout:
		if result.Reason == ProbeReasonTimeout {
			return result
		}
	}
	return ProbeResult{Dependency: result.Dependency, Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
}

type redisProbeClient interface {
	Ping(context.Context) *redisclient.StatusCmd
	Time(context.Context) *redisclient.TimeCmd
	ConfigGet(context.Context, string) *redisclient.MapStringStringCmd
	FunctionList(context.Context, redisclient.FunctionListQuery) *redisclient.FunctionListCmd
	ScriptExists(context.Context, ...string) *redisclient.BoolSliceCmd
}

type redisDependencyProbe struct {
	client redisProbeClient
	config config.RedisConfig
}

// NewRedisDependencyProbe constructs a read-only Redis readiness probe. It
// never exposes a loading/replacing command, so startup and monitoring cannot
// silently mutate a shared Redis Function library or Lua script.
func NewRedisDependencyProbe(client redisProbeClient, value config.RedisConfig) (DependencyProbe, error) {
	if client == nil {
		return nil, fmt.Errorf("Redis readiness client is required")
	}
	if value.AdmissionMode != "function" && value.AdmissionMode != "lua" {
		return nil, fmt.Errorf("Redis readiness admission mode is invalid")
	}
	if value.RequiredPersistence != "aof_and_rdb" && value.RequiredPersistence != "aof" && value.RequiredPersistence != "rdb" {
		return nil, fmt.Errorf("Redis readiness persistence policy is invalid")
	}
	return &redisDependencyProbe{client: client, config: value}, nil
}

func (probe *redisDependencyProbe) Probe(ctx context.Context) ProbeResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if probe == nil || probe.client == nil {
		return redisUnavailable(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return redisUnavailable(err)
	}
	if _, err := probe.client.Ping(ctx).Result(); err != nil {
		return redisUnavailable(err)
	}
	serverTime, err := probe.client.Time(ctx).Result()
	if err != nil || serverTime.IsZero() {
		if err == nil {
			err = errors.New("Redis returned no server time")
		}
		return redisUnavailable(err)
	}
	policy, err := redisSetting(ctx, probe.client, "maxmemory-policy")
	if err != nil {
		return redisUnavailable(err)
	}
	if !strings.EqualFold(strings.TrimSpace(policy), "noeviction") {
		return redisPolicyMismatch()
	}
	appendOnly, err := redisSetting(ctx, probe.client, "appendonly")
	if err != nil {
		return redisUnavailable(err)
	}
	save, err := redisSetting(ctx, probe.client, "save")
	if err != nil {
		return redisUnavailable(err)
	}
	if requiresAOF(probe.config.RequiredPersistence) && !strings.EqualFold(strings.TrimSpace(appendOnly), "yes") {
		return redisPolicyMismatch()
	}
	if requiresRDB(probe.config.RequiredPersistence) && strings.TrimSpace(save) == "" {
		return redisPolicyMismatch()
	}
	switch probe.config.AdmissionMode {
	case "function":
		return probe.verifyFunction(ctx)
	case "lua":
		return probe.verifyLua(ctx)
	default:
		return redisPolicyMismatch()
	}
}

func (probe *redisDependencyProbe) verifyFunction(ctx context.Context) ProbeResult {
	if probe.config.FunctionLibrary != redisstore.AdmissionFunctionLibrary || probe.config.AdmissionVersion != redisstore.AdmissionFunctionVersion || probe.config.AdmissionDigest != redisstore.AdmissionFunctionDigest() {
		return redisPolicyMismatch()
	}
	libraries, err := probe.client.FunctionList(ctx, redisclient.FunctionListQuery{LibraryNamePattern: probe.config.FunctionLibrary, WithCode: true}).Result()
	if err != nil {
		return redisUnavailable(err)
	}
	for _, library := range libraries {
		if library.Name != probe.config.FunctionLibrary {
			continue
		}
		digest := sha256.Sum256([]byte(library.Code))
		if hex.EncodeToString(digest[:]) != probe.config.AdmissionDigest {
			return redisPolicyMismatch()
		}
		for _, function := range library.Functions {
			if function.Name == probe.config.AdmissionVersion {
				return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusReady, Reason: ProbeReasonReady}
			}
		}
		return redisPolicyMismatch()
	}
	return redisPolicyMismatch()
}

func (probe *redisDependencyProbe) verifyLua(ctx context.Context) ProbeResult {
	if probe.config.AdmissionVersion != redisstore.AdmissionFunctionVersion || probe.config.AdmissionDigest != redisstore.AdmissionLuaDigest() {
		return redisPolicyMismatch()
	}
	exists, err := probe.client.ScriptExists(ctx, redisstore.AdmissionLuaSHA1()).Result()
	if err != nil {
		return redisUnavailable(err)
	}
	if len(exists) != 1 || !exists[0] {
		return redisPolicyMismatch()
	}
	return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusReady, Reason: ProbeReasonReady}
}

func redisSetting(ctx context.Context, client redisProbeClient, name string) (string, error) {
	values, err := client.ConfigGet(ctx, name).Result()
	if err != nil {
		return "", err
	}
	value, ok := values[name]
	if !ok {
		return "", errors.New("Redis config setting is absent")
	}
	return value, nil
}

func requiresAOF(policy string) bool { return policy == "aof" || policy == "aof_and_rdb" }

func requiresRDB(policy string) bool { return policy == "rdb" || policy == "aof_and_rdb" }

func redisUnavailable(err error) ProbeResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusTimeout, Reason: ProbeReasonTimeout}
	}
	return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
}

func redisPolicyMismatch() ProbeResult {
	return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusPolicy, Reason: ProbeReasonPolicyMismatch}
}

type bucketProbe interface {
	ProbeBucket(context.Context) error
}

type blobDependencyProbe struct{ bucket bucketProbe }

// NewBlobDependencyProbe wraps a bucket-only capability. It deliberately has
// no object/key arguments, making tenant reads and writes impossible here.
func NewBlobDependencyProbe(bucket bucketProbe) (DependencyProbe, error) {
	if bucket == nil {
		return nil, fmt.Errorf("blob readiness probe is required")
	}
	return &blobDependencyProbe{bucket: bucket}, nil
}

func (probe *blobDependencyProbe) Probe(ctx context.Context) ProbeResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if probe == nil || probe.bucket == nil {
		return blobUnavailable(ctx.Err())
	}
	if err := probe.bucket.ProbeBucket(ctx); err != nil {
		return blobUnavailable(err)
	}
	return ProbeResult{Dependency: DependencyBlobStore, Status: ProbeStatusReady, Reason: ProbeReasonReady}
}

func blobUnavailable(err error) ProbeResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return ProbeResult{Dependency: DependencyBlobStore, Status: ProbeStatusTimeout, Reason: ProbeReasonTimeout}
	}
	return ProbeResult{Dependency: DependencyBlobStore, Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
}
