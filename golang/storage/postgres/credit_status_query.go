package postgres

// This file contains the endpoint-level credit/billing read side. It reads
// only the current provider-route projection and the event referenced by each
// projection row. Provider refreshes, inference calls, cursor signing, and
// wire serialization remain outside the PostgreSQL repository.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/control"
)

const (
	DefaultCreditStatusPageSize = 100
	MaxCreditStatusPageSize     = 1000
)

// CreditStatusListOptions describes the unsigned database portion of a
// credit-status query. Endpoint IDs are the stable keyset position because a
// configured endpoint may have more than one route projection; the query
// chooses the latest projection for each endpoint deterministically.
type CreditStatusListOptions struct {
	ConfigDigest     [32]byte
	Provider         string
	EndpointID       string
	IncludeOK        bool
	AfterEndpointKey string
	Limit            int
}

func (options *CreditStatusListOptions) normalize() error {
	if options == nil {
		return errors.New("credit status list options are nil")
	}
	if options.ConfigDigest == ([32]byte{}) {
		return errors.New("credit status list config digest is required")
	}
	for name, value := range map[string]string{
		"provider":    options.Provider,
		"endpoint_id": options.EndpointID,
	} {
		if value == "" {
			continue
		}
		if err := validateProviderStatusQueryIdentifier(name, value); err != nil {
			return err
		}
	}
	if options.AfterEndpointKey != "" {
		if _, _, err := splitCreditStatusKey(options.AfterEndpointKey); err != nil {
			return err
		}
	}
	if options.Limit == 0 {
		options.Limit = DefaultCreditStatusPageSize
	}
	if options.Limit < 1 || options.Limit > MaxCreditStatusPageSize {
		return fmt.Errorf("credit status page size must be between 1 and %d", MaxCreditStatusPageSize)
	}
	return nil
}

// ListCreditStatuses reads one current status projection per provider/endpoint
// identity in stable key order. The DISTINCT ON ordering is deliberate: when a
// configuration has several routes for one endpoint, the newest observed
// projection wins and route_id provides a deterministic tie-breaker. Healthy
// rows are omitted unless IncludeOK is true. The event join exposes only the
// bounded source/code fields required for safe credit evidence.
func (repository ProviderStatusRepository) ListCreditStatuses(ctx context.Context, options CreditStatusListOptions) (control.CreditStatusPage, error) {
	var page control.CreditStatusPage
	if err := repository.validate(); err != nil {
		return page, err
	}
	if err := options.normalize(); err != nil {
		return page, err
	}
	afterProvider, afterEndpoint, err := splitCreditStatusKey(options.AfterEndpointKey)
	if err != nil {
		return page, err
	}
	routes, err := repository.Namespace.Render("provider_route_status")
	if err != nil {
		return page, err
	}
	events, err := repository.Namespace.Render("provider_status_events")
	if err != nil {
		return page, err
	}
	query := "WITH current_endpoint AS (" +
		"SELECT DISTINCT ON (r.provider, r.endpoint_id) r.provider, r.endpoint_id, r.credit_state, r.billing_state, r.credit_confirmed_at, r.observed_at, r.route_id, incident.source, incident.safe_error_code, incident.provider_code " +
		"FROM " + routes + " r LEFT JOIN LATERAL (" +
		"SELECT e.source, e.safe_error_code, e.provider_code FROM " + events + " e " +
		"WHERE e.config_digest = r.config_digest AND e.route_id = r.route_id " +
		"AND ((r.credit_state IN ('low','exhausted') AND e.credit_state IN ('low','exhausted')) " +
		"OR (r.billing_state = 'issue' AND e.billing_state = 'issue')) " +
		"ORDER BY e.observed_at DESC, e.event_id DESC LIMIT 1" +
		") incident ON TRUE " +
		"WHERE r.config_digest = $1 AND ($2 = '' OR r.provider = $2) AND ($3 = '' OR r.endpoint_id = $3) " +
		"ORDER BY r.provider, r.endpoint_id, r.observed_at DESC, r.route_id DESC" +
		") SELECT provider, endpoint_id, credit_state, billing_state, credit_confirmed_at, source, safe_error_code, provider_code " +
		"FROM current_endpoint WHERE ($4 OR credit_state <> 'ok' OR billing_state <> 'ok') AND ($5 = '' OR (provider > $5 OR (provider = $5 AND endpoint_id > $6))) " +
		"ORDER BY provider, endpoint_id LIMIT $7"
	rows, err := repository.Pool.Query(ctx, query, options.ConfigDigest[:], options.Provider, options.EndpointID, options.IncludeOK, afterProvider, afterEndpoint, options.Limit+1)
	if err != nil {
		return page, redactPostgresError(fmt.Errorf("list credit statuses: %w", err))
	}
	defer rows.Close()
	for rows.Next() {
		var provider, endpoint, credit, billing string
		var source *string
		var confirmedAt *time.Time
		var safeErrorCode, providerCode *string
		if err := rows.Scan(&provider, &endpoint, &credit, &billing, &confirmedAt, &source, &safeErrorCode, &providerCode); err != nil {
			return control.CreditStatusPage{}, redactPostgresError(fmt.Errorf("scan credit status: %w", err))
		}
		var confirmed time.Time
		if confirmedAt != nil {
			confirmed = confirmedAt.UTC()
		}
		evidenceSource := control.SourceInference
		if source != nil {
			evidenceSource = control.Source(*source)
		}
		status, err := control.NewCreditStatus(provider, endpoint, control.CreditState(credit), control.BillingState(billing), confirmed, evidenceSource, nullableStringValue(safeErrorCode), nullableStringValue(providerCode))
		if err != nil {
			return control.CreditStatusPage{}, fmt.Errorf("PostgreSQL credit status is invalid: %w", err)
		}
		page.Endpoints = append(page.Endpoints, status)
	}
	if err := rows.Err(); err != nil {
		return control.CreditStatusPage{}, redactPostgresError(fmt.Errorf("read credit statuses: %w", err))
	}
	if len(page.Endpoints) > options.Limit {
		page.NextEndpointKey = page.Endpoints[options.Limit-1].Key()
		page.Endpoints = page.Endpoints[:options.Limit]
	}
	if err := page.Validate(); err != nil {
		return control.CreditStatusPage{}, err
	}
	return page, nil
}

func splitCreditStatusKey(key string) (provider, endpoint string, err error) {
	if key == "" {
		return "", "", nil
	}
	provider, endpoint, ok := strings.Cut(key, "\x00")
	if !ok || provider == "" || endpoint == "" {
		return "", "", errors.New("credit status continuation key is invalid")
	}
	if err := validateProviderStatusQueryIdentifier("after_provider", provider); err != nil {
		return "", "", err
	}
	if err := validateProviderStatusQueryIdentifier("after_endpoint", endpoint); err != nil {
		return "", "", err
	}
	return provider, endpoint, nil
}

func nullableStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
