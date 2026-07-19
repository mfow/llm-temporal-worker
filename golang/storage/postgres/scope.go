package postgres

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Scope struct {
	ID          uuid.UUID
	TenantHMAC  [keyDigestBytes]byte
	ProjectHMAC [keyDigestBytes]byte
}

// ScopeKeyring derives tenant/project lookup values with one active key. A
// deployment can retain old keys in Keys while it performs an explicit,
// separately audited re-key operation; this foundation never rewrites rows
// implicitly during reads.
type ScopeKeyring struct {
	ActiveVersion string
	Keys          map[string][]byte
}

func (ring ScopeKeyring) activeKey() ([]byte, error) {
	key, ok := ring.Keys[ring.ActiveVersion]
	if !ok || len(key) != keyDigestBytes {
		return nil, fmt.Errorf("scope HMAC key version %q is unavailable", ring.ActiveVersion)
	}
	return key, nil
}

func (ring ScopeKeyring) Derive(tenant, project string) ([keyDigestBytes]byte, [keyDigestBytes]byte, error) {
	var tenantDigest, projectDigest [keyDigestBytes]byte
	key, err := ring.activeKey()
	if err != nil {
		return tenantDigest, projectDigest, err
	}
	tenantDigest, err = ScopeHMAC(key, tenant)
	if err != nil {
		return tenantDigest, projectDigest, err
	}
	projectDigest, err = ScopeHMAC(key, project)
	if err != nil {
		return tenantDigest, projectDigest, err
	}
	return tenantDigest, projectDigest, nil
}

// ScopeRepository owns only scope metadata. Every query binds tenant/project
// values as HMACs and qualifies the relation from the validated Namespace.
type ScopeRepository struct {
	Pool      *pgxpool.Pool
	Namespace Namespace
	Keys      ScopeKeyring
	NewID     func() (uuid.UUID, error)
}

func (repository ScopeRepository) validate() error {
	if repository.Pool == nil {
		return errors.New("scope repository pool is nil")
	}
	if err := repository.Namespace.Validate(); err != nil {
		return err
	}
	if _, err := repository.Keys.activeKey(); err != nil {
		return err
	}
	if repository.NewID == nil {
		return errors.New("scope repository UUID generator is nil")
	}
	return nil
}

func (repository ScopeRepository) Ensure(ctx context.Context, tenant, project string) (Scope, error) {
	var scope Scope
	if err := repository.validate(); err != nil {
		return scope, err
	}
	tenantDigest, projectDigest, err := repository.Keys.Derive(tenant, project)
	if err != nil {
		return scope, err
	}
	scopeID, err := repository.NewID()
	if err != nil {
		return scope, fmt.Errorf("generate scope id: %w", err)
	}
	if scopeID == uuid.Nil {
		return scope, errors.New("scope UUID generator returned nil")
	}
	relation, err := repository.Namespace.Render("scopes")
	if err != nil {
		return scope, err
	}
	query := "INSERT INTO " + relation + " (scope_id, tenant_hmac, project_hmac) VALUES ($1, $2, $3) " +
		"ON CONFLICT (tenant_hmac, project_hmac) DO UPDATE SET deleted_at = NULL " +
		"RETURNING scope_id, tenant_hmac, project_hmac"
	var tenantStored, projectStored []byte
	if err := repository.Pool.QueryRow(ctx, query, scopeID, tenantDigest[:], projectDigest[:]).Scan(&scope.ID, &tenantStored, &projectStored); err != nil {
		return scope, redactPostgresError(fmt.Errorf("ensure PostgreSQL scope: %w", err))
	}
	if len(tenantStored) != keyDigestBytes || len(projectStored) != keyDigestBytes {
		return Scope{}, errors.New("PostgreSQL scope HMAC has invalid length")
	}
	copy(scope.TenantHMAC[:], tenantStored)
	copy(scope.ProjectHMAC[:], projectStored)
	if !hmac.Equal(scope.TenantHMAC[:], tenantDigest[:]) || !hmac.Equal(scope.ProjectHMAC[:], projectDigest[:]) {
		return Scope{}, errors.New("PostgreSQL scope HMAC does not match configured key")
	}
	return scope, nil
}

func DefaultScopeRepository(pool *pgxpool.Pool, namespace Namespace, keys ScopeKeyring) ScopeRepository {
	return ScopeRepository{Pool: pool, Namespace: namespace, Keys: keys, NewID: UUIDv7}
}
