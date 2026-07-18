package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mfow/llm-temporal-worker/golang/config"
	"go.temporal.io/sdk/client"
)

// TemporalClientFactory is the process boundary for creating a Temporal
// client. Runtime construction uses this interface so tests and local tools
// can avoid a live cluster while production uses the official SDK client.
type TemporalClientFactory interface {
	New(context.Context, config.Config) (client.Client, error)
}

// TemporalClientFactoryFunc adapts a function to TemporalClientFactory.
type TemporalClientFactoryFunc func(context.Context, config.Config) (client.Client, error)

func (function TemporalClientFactoryFunc) New(ctx context.Context, value config.Config) (client.Client, error) {
	return function(ctx, value)
}

// DefaultTemporalClientFactory creates an eagerly connected SDK client using
// only non-secret Temporal configuration. Credentials, if a deployment adds
// them through the SDK, stay inside the SDK's credential boundary.
type DefaultTemporalClientFactory struct {
	// Identity overrides the generated worker/client identity. It is useful for
	// tests and for deployments that already provide a stable identity.
	Identity string
	// ReadFile is injectable for tests. The default reads bounded local files.
	ReadFile func(string) ([]byte, error)
}

func (factory DefaultTemporalClientFactory) New(ctx context.Context, value config.Config) (client.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := client.Options{
		HostPort:  value.Temporal.Target,
		Namespace: value.Temporal.Namespace,
		Identity:  factory.identity(value.Temporal.IdentityPrefix),
	}
	if value.Temporal.TLS.Enabled {
		readFile := factory.ReadFile
		if readFile == nil {
			readFile = readBoundedFile
		}
		tlsConfig, err := loadTLSConfig(value.Temporal.TLS, readFile)
		if err != nil {
			return nil, err
		}
		options.ConnectionOptions.TLS = tlsConfig
	}
	return client.DialContext(ctx, options)
}

func (factory DefaultTemporalClientFactory) identity(prefix string) string {
	if factory.Identity != "" {
		return factory.Identity
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "worker"
	}
	hostname = sanitizeIdentity(hostname)
	if prefix == "" {
		prefix = "llmtw"
	}
	return fmt.Sprintf("%s-%s-%d", sanitizeIdentity(prefix), hostname, os.Getpid())
}

func sanitizeIdentity(value string) string {
	var builder strings.Builder
	for _, runeValue := range value {
		switch {
		case runeValue >= 'a' && runeValue <= 'z', runeValue >= 'A' && runeValue <= 'Z', runeValue >= '0' && runeValue <= '9', runeValue == '-', runeValue == '_', runeValue == '.':
			builder.WriteRune(runeValue)
		default:
			builder.WriteByte('-')
		}
	}
	if builder.Len() == 0 {
		return "worker"
	}
	return builder.String()
}

func loadTLSConfig(value config.TLSConfig, readFile func(string) ([]byte, error)) (*tls.Config, error) {
	if !value.Enabled {
		return nil, nil
	}
	if readFile == nil {
		return nil, errors.New("Temporal TLS CA reader is unavailable")
	}
	encoded, err := readFile(value.CAFile)
	if err != nil {
		return nil, errors.New("read Temporal TLS CA certificate")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(encoded) {
		return nil, errors.New("Temporal TLS CA certificate is invalid")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: value.ServerName,
		RootCAs:    pool,
	}, nil
}

func readBoundedFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("file path is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("file is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("file is unavailable")
	}
	defer file.Close()
	const maxBytes = 1 << 20
	value, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(value) > maxBytes {
		return nil, errors.New("file exceeds the safe size limit")
	}
	return value, nil
}
