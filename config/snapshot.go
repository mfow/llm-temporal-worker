package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

// ReferenceResolver is the boundary for optional local reference checks. It
// may verify catalog files or secret-reference policy, but it must not mutate
// the configuration with secret values.
type ReferenceResolver interface {
	Resolve(context.Context, *Config) error
}

type ReferenceResolverFunc func(context.Context, *Config) error

func (function ReferenceResolverFunc) Resolve(ctx context.Context, config *Config) error {
	return function(ctx, config)
}

// Snapshot is an immutable, validated, non-secret configuration view. Config
// returns a deep copy so callers cannot mutate a snapshot after publication.
type Snapshot struct {
	config    Config
	canonical []byte
	digest    [32]byte
	digestHex string
}

// Compile performs parse, defaulting, validation, optional reference checks,
// and canonical non-secret digesting before constructing a snapshot.
func Compile(ctx context.Context, data []byte, resolver ReferenceResolver) (*Snapshot, error) {
	config, err := Load(data)
	if err != nil {
		return nil, err
	}
	if resolver != nil {
		if err := resolver.Resolve(ctx, &config); err != nil {
			return nil, fmt.Errorf("configuration references: %w", err)
		}
		if err := config.Validate(); err != nil {
			return nil, err
		}
	}
	canonical, err := config.canonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("configuration canonicalization: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return &Snapshot{
		config:    config.Clone(),
		canonical: append([]byte(nil), canonical...),
		digest:    digest,
		digestHex: hex.EncodeToString(digest[:]),
	}, nil
}

// Config returns an independent copy of the compiled configuration.
func (snapshot *Snapshot) Config() Config {
	if snapshot == nil {
		return Config{}
	}
	return snapshot.config.Clone()
}

func (snapshot *Snapshot) APIVersion() string {
	if snapshot == nil {
		return ""
	}
	return snapshot.config.Version
}

// ConfigVersion is the stable lowercase SHA-256 of the canonical non-secret
// effective configuration.
func (snapshot *Snapshot) ConfigVersion() string {
	if snapshot == nil {
		return ""
	}
	return snapshot.digestHex
}

func (snapshot *Snapshot) Digest() [32]byte {
	if snapshot == nil {
		return [32]byte{}
	}
	return snapshot.digest
}

// Canonical returns a copy of the canonical non-secret configuration JSON.
func (snapshot *Snapshot) Canonical() []byte {
	if snapshot == nil {
		return nil
	}
	return append([]byte(nil), snapshot.canonical...)
}

// SnapshotSource atomically publishes only complete valid snapshots. A
// failed reload leaves the previous pointer untouched.
type SnapshotSource struct {
	current atomic.Pointer[Snapshot]
}

func NewSnapshotSource(initial *Snapshot) *SnapshotSource {
	source := &SnapshotSource{}
	if initial != nil {
		source.current.Store(initial)
	}
	return source
}

func (source *SnapshotSource) Current() *Snapshot {
	if source == nil {
		return nil
	}
	return source.current.Load()
}

func (source *SnapshotSource) Reload(ctx context.Context, data []byte, resolver ReferenceResolver) error {
	if source == nil {
		return fmt.Errorf("snapshot source is nil")
	}
	next, err := Compile(ctx, data, resolver)
	if err != nil {
		return err
	}
	source.current.Store(next)
	return nil
}
