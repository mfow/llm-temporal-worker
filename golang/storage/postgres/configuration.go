package postgres

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"
)

// TLSOptions describes verified PostgreSQL TLS. InsecureSkipVerify is not
// exposed intentionally: production callers must verify the configured
// server name and CA. Development deployments may leave Enabled false, while
// config validation rejects that choice for production.
type TLSOptions struct {
	Enabled    bool
	ServerName string
	CAFile     string
}

func (options TLSOptions) tlsConfig() (*tls.Config, error) {
	if !options.Enabled {
		return nil, nil
	}
	if options.ServerName == "" || options.CAFile == "" {
		return nil, fmt.Errorf("PostgreSQL TLS requires server name and CA file")
	}
	contents, err := os.ReadFile(options.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		return nil, fmt.Errorf("PostgreSQL CA file contains no certificates")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: options.ServerName, RootCAs: roots}, nil
}

// PoolOptions is the validated, secret-resolved connection configuration used
// by NewPool. Password is consumed only while constructing pgx config and is
// never included in returned errors or diagnostics.
type PoolOptions struct {
	Namespace        Namespace
	Addresses        []string
	Username         string
	Password         string
	TLS              TLSOptions
	MaxConnections   int32
	MinConnections   int32
	DialTimeout      time.Duration
	StatementTimeout time.Duration
	LockTimeout      time.Duration
	IdleTxTimeout    time.Duration
	ApplicationName  string
}

func (options PoolOptions) validate() error {
	if err := options.Namespace.Validate(); err != nil {
		return err
	}
	if len(options.Addresses) == 0 {
		return fmt.Errorf("PostgreSQL addresses must not be empty")
	}
	for _, address := range options.Addresses {
		if strings.TrimSpace(address) == "" || strings.ContainsAny(address, "\r\n\x00") {
			return fmt.Errorf("PostgreSQL address is empty or contains control characters")
		}
	}
	if options.Username == "" {
		return fmt.Errorf("PostgreSQL username is empty")
	}
	if options.MaxConnections <= 0 || options.MaxConnections > 100000 {
		return fmt.Errorf("PostgreSQL max connections is outside safe bounds")
	}
	if options.MinConnections < 0 || options.MinConnections > options.MaxConnections {
		return fmt.Errorf("PostgreSQL min connections is outside safe bounds")
	}
	for name, value := range map[string]time.Duration{
		"dial timeout":             options.DialTimeout,
		"statement timeout":        options.StatementTimeout,
		"lock timeout":             options.LockTimeout,
		"idle transaction timeout": options.IdleTxTimeout,
	} {
		if value < time.Millisecond || value > 24*time.Hour {
			return fmt.Errorf("PostgreSQL %s must be between 1ms and 24h", name)
		}
	}
	if strings.ContainsAny(options.ApplicationName, "\r\n\x00") {
		return fmt.Errorf("PostgreSQL application name contains control characters")
	}
	return nil
}

func durationMilliseconds(value time.Duration) string {
	return fmt.Sprintf("%dms", value/time.Millisecond)
}

func redactPostgresError(err error) error {
	if err == nil {
		return nil
	}
	// pgx errors can include a host or database, but never include the resolved
	// password once it has been redacted from the connection string. Keep the
	// operation context while avoiding SQL parameters and credentials.
	message := err.Error()
	for _, marker := range []string{"password=", "password%3D"} {
		if index := strings.Index(strings.ToLower(message), marker); index >= 0 {
			message = message[:index] + "password=[redacted]"
		}
	}
	return fmt.Errorf("PostgreSQL operation failed: %s", message)
}
