package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	redisclient "github.com/redis/go-redis/v9"
)

const (
	AdmissionFunctionLibrary = "llmtw_admission_v1"
	AdmissionFunctionVersion = "admission/v1"
)

type FunctionMetadata struct {
	Library string
	Version string
	Digest  string
}

// FunctionInvoker is the deliberately small seam used by the stores. The
// production implementation executes the immutable embedded Lua script via
// EVALSHA/EVAL; tests can provide an offline command/function harness without
// importing a Redis server.
type FunctionInvoker interface {
	Run(context.Context, string, []string, ...string) ([]any, error)
}

// StringReader is the read-only subset needed to resolve operation and
// continuation records after a command/network error. The store never blindly
// retries a mutation; callers resolve the result by reading the record.
type StringReader interface {
	Get(context.Context, string) (string, error)
}

type redisInvoker struct{ client redisclient.Scripter }

func (invoker redisInvoker) Run(ctx context.Context, name string, keys []string, args ...string) ([]any, error) {
	if name != AdmissionFunctionVersion {
		return nil, fmt.Errorf("unsupported Redis function %q", name)
	}
	values := make([]interface{}, len(args))
	for index, value := range args {
		values[index] = value
	}
	result, err := admissionScript.Run(ctx, invoker.client, keys, values...).Result()
	if err != nil {
		return nil, err
	}
	array, ok := result.([]interface{})
	if !ok {
		return nil, fmt.Errorf("Redis function returned an invalid result")
	}
	return array, nil
}

type redisReader struct {
	client interface {
		Get(context.Context, string) *redisclient.StringCmd
	}
}

func (reader redisReader) Get(ctx context.Context, key string) (string, error) {
	return reader.client.Get(ctx, key).Result()
}

var admissionScript = redisclient.NewScript(admissionFunctionSource)

// AdmissionFunctionDigest is checked by readiness/deployment tooling before a
// worker starts polling. It is a SHA-256 of the exact immutable source bytes.
func AdmissionFunctionDigest() string {
	digest := sha256.Sum256([]byte(admissionFunctionSource))
	return hex.EncodeToString(digest[:])
}

func AdmissionFunctionMetadata() FunctionMetadata {
	return FunctionMetadata{Library: AdmissionFunctionLibrary, Version: AdmissionFunctionVersion, Digest: AdmissionFunctionDigest()}
}

// AdmissionFunctionSource returns a copy of the embedded source for startup
// verification and offline harnesses.
func AdmissionFunctionSource() string { return strings.Clone(admissionFunctionSource) }
