package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/budget"
	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/engine"
	"github.com/mfow/llm-temporal-worker/internal/catalog"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"github.com/mfow/llm-temporal-worker/pricing"
	"github.com/mfow/llm-temporal-worker/routing"
)

// SnapshotLoader is the catalog-to-engine composition boundary. Catalog
// loading and verification stay in internal/catalog; this interface only
// publishes the complete immutable engine snapshot consumed by one request.
type SnapshotLoader interface {
	Load(context.Context, *config.Snapshot) (engine.Snapshot, error)
}

type SnapshotLoaderFunc func(context.Context, *config.Snapshot) (engine.Snapshot, error)

func (function SnapshotLoaderFunc) Load(ctx context.Context, snapshot *config.Snapshot) (engine.Snapshot, error) {
	return function(ctx, snapshot)
}

// CatalogSnapshotLoader is the default loader used by the production
// composition layer. It consumes verified capability and pricing catalogs and
// compiles config routes into routing.Catalog without duplicating catalog
// parsing or digest verification.
type CatalogSnapshotLoader struct {
	CatalogOptions catalog.Options
	Clock          func() time.Time
}

func (loader CatalogSnapshotLoader) Load(ctx context.Context, snapshot *config.Snapshot) (engine.Snapshot, error) {
	if snapshot == nil {
		return engine.Snapshot{}, fmt.Errorf("configuration snapshot is required")
	}
	if err := ctx.Err(); err != nil {
		return engine.Snapshot{}, err
	}
	clock := loader.Clock
	if clock == nil {
		clock = time.Now
	}
	value := snapshot.Config()
	bundle, err := catalog.LoadWithOptions(value, loader.CatalogOptions)
	if err != nil {
		return engine.Snapshot{}, fmt.Errorf("load verified catalogs: %w", err)
	}
	now := clock()
	price, err := mergePricingCatalogs(bundle, snapshot.ConfigVersion())
	if err != nil {
		return engine.Snapshot{}, err
	}
	routes, err := compileRoutes(value, bundle, now)
	if err != nil {
		return engine.Snapshot{}, err
	}
	policies, err := compileBudgetPolicies(value)
	if err != nil {
		return engine.Snapshot{}, err
	}
	return engine.Snapshot{
		Version:               snapshot.ConfigVersion(),
		Routes:                routes,
		Health:                routing.HealthView{},
		Prices:                pricing.NewResolver(price),
		BudgetPolicies:        policies,
		Environment:           value.Environment,
		ReservationLease:      time.Duration(value.State.ReservationLease),
		OperationRetention:    time.Duration(value.State.OperationTerminalRetention),
		ContinuationRetention: time.Duration(value.State.ContinuationRetention),
	}, nil
}

func mergePricingCatalogs(bundle catalog.Bundle, configVersion string) (pricing.Catalog, error) {
	ids := make([]string, 0, len(bundle.Pricing))
	for id := range bundle.Pricing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return pricing.Catalog{}, fmt.Errorf("verified pricing catalogs are required")
	}
	currency := ""
	entries := make([]pricing.Entry, 0)
	for _, id := range ids {
		catalogValue := bundle.Pricing[id].Catalog
		if currency == "" {
			currency = catalogValue.Currency
		} else if currency != catalogValue.Currency {
			return pricing.Catalog{}, fmt.Errorf("pricing catalogs use multiple currencies")
		}
		entries = append(entries, catalogValue.Entries...)
	}
	version := "runtime-prices"
	if strings.TrimSpace(configVersion) != "" {
		version += "/" + configVersion
	}
	merged, err := pricing.CompileCatalog(version, currency, entries)
	if err != nil {
		return pricing.Catalog{}, fmt.Errorf("merge pricing catalogs: %w", err)
	}
	return merged, nil
}

func compileRoutes(value config.Config, bundle catalog.Bundle, now time.Time) (routing.Catalog, error) {
	models := make(map[string]routing.Model, len(value.Models))
	modelNames := make([]string, 0, len(value.Models))
	for name := range value.Models {
		modelNames = append(modelNames, name)
	}
	sort.Strings(modelNames)
	for _, modelName := range modelNames {
		modelValue := value.Models[modelName]
		routes := make([]routing.Route, 0, len(modelValue.Routes))
		for index, routeValue := range modelValue.Routes {
			endpoint, ok := value.Endpoints[routeValue.Endpoint]
			if !ok {
				return routing.Catalog{}, fmt.Errorf("model %q route %d references missing endpoint %q", modelName, index, routeValue.Endpoint)
			}
			profile, ok := bundle.Capabilities[endpoint.CapabilityProfile]
			if !ok {
				return routing.Catalog{}, fmt.Errorf("endpoint %q capability profile %q is unavailable", routeValue.Endpoint, endpoint.CapabilityProfile)
			}
			family := endpointFamily(endpoint.Family)
			if profile.Family != family {
				return routing.Catalog{}, fmt.Errorf("endpoint %q capability family %q does not match %q", routeValue.Endpoint, profile.Family, family)
			}
			if profile.Model != routeValue.Model {
				return routing.Catalog{}, fmt.Errorf("route %q model %q does not match capability profile %q model %q", routeValue.ID, routeValue.Model, endpoint.CapabilityProfile, profile.Model)
			}
			providerName, routeRegion, priceVersion, err := routePriceIdentity(bundle, routeValue.Endpoint, endpoint, routeValue.Model, routeValue.Classes, now)
			if err != nil {
				return routing.Catalog{}, fmt.Errorf("model %q route %q: %w", modelName, routeValue.ID, err)
			}
			providerTiers := make(map[llm.ServiceClass]string, len(routeValue.Classes))
			for _, class := range routeValue.Classes {
				tier := endpoint.ServiceClasses[class].ProviderValue
				if tier == "" {
					return routing.Catalog{}, fmt.Errorf("route class %q has no provider tier", class)
				}
				providerTiers[class] = tier
			}
			extensions := make([]string, 0, len(endpoint.Extensions))
			for name := range endpoint.Extensions {
				extensions = append(extensions, name)
			}
			sort.Strings(extensions)
			routes = append(routes, routing.Route{
				ID:             routeValue.ID,
				EndpointID:     routeValue.Endpoint,
				Provider:       providerName,
				Family:         string(family),
				Region:         routeRegion,
				AccountRegion:  endpoint.AccountRegion,
				Model:          routeValue.Model,
				ModelLineage:   routeValue.Model,
				Classes:        append([]llm.ServiceClass(nil), routeValue.Classes...),
				ProviderTiers:  providerTiers,
				AllowedTenants: append([]string(nil), modelValue.AllowedTenants...),
				AllowedRegions: append([]string(nil), modelValue.DataRegions...),
				Capabilities:   routingCapabilities(profile.Set),
				PriceVersion:   priceVersion,
				PriceAvailable: true,
				ExtensionNames: extensions,
			})
		}
		models[modelName] = routing.Model{Name: modelName, Routes: routes}
	}
	return routing.CompileCatalog(value.Version, models)
}

func routePriceIdentity(bundle catalog.Bundle, endpointID string, endpoint config.EndpointConfig, model string, classes []llm.ServiceClass, now time.Time) (string, string, string, error) {
	// Catalog.Load has already checked that the endpoint has at least one
	// family/endpoint price. Route compilation tightens that check to every
	// concrete model/class so admission can never reserve against a missing
	// quote later in the request.
	priceCatalog, ok := bundle.Pricing[endpoint.PriceCatalog]
	if !ok {
		return "", "", "", fmt.Errorf("price catalog %q is unavailable", endpoint.PriceCatalog)
	}
	var providerName, routeRegion, priceVersion string
	family := endpointFamily(endpoint.Family)
	for _, class := range classes {
		tier := endpoint.ServiceClasses[class].ProviderValue
		var found *pricing.Entry
		for index := range priceCatalog.Catalog.Entries {
			entry := &priceCatalog.Catalog.Entries[index]
			// Keep the identity checks explicit: provider names are catalog data,
			// while endpoint/family/model/tier are operator configuration.
			if entry.EndpointID != endpointID || entry.Family != string(family) || entry.Model != model || entry.ProviderTier != tier || !entry.Active(now) {
				continue
			}
			if endpoint.Region != "" && entry.Region != endpoint.Region {
				continue
			}
			if found != nil {
				return "", "", "", fmt.Errorf("multiple active price entries for model %q tier %q", model, tier)
			}
			copy := *entry
			found = &copy
		}
		if found == nil {
			return "", "", "", fmt.Errorf("no active price entry for model %q tier %q", model, tier)
		}
		if providerName == "" {
			providerName = found.Provider
		} else if providerName != found.Provider {
			return "", "", "", fmt.Errorf("price entries use multiple providers")
		}
		if routeRegion == "" {
			routeRegion = found.Region
		} else if routeRegion != found.Region {
			return "", "", "", fmt.Errorf("price entries use multiple regions")
		}
		entryVersion := found.Version
		if entryVersion == "" {
			entryVersion = priceCatalog.Version
		}
		if priceVersion == "" {
			priceVersion = entryVersion
		} else if priceVersion != entryVersion {
			return "", "", "", fmt.Errorf("price entries use multiple versions")
		}
	}
	return providerName, routeRegion, priceVersion, nil
}

func compileBudgetPolicies(value config.Config) ([]budget.Policy, error) {
	policies := make([]budget.Policy, 0, len(value.Budgets.Policies))
	for _, policyValue := range value.Budgets.Policies {
		policy := budget.Policy{ID: policyValue.ID, Match: budget.Matcher{Tenant: policyValue.Match.Tenant, Environment: policyValue.Match.Environment}}
		policy.Windows = make([]budget.Window, 0, len(policyValue.Windows))
		for index, windowValue := range policyValue.Windows {
			policy.Windows = append(policy.Windows, budget.Window{ID: fmt.Sprintf("%s/%d", policyValue.ID, index), Duration: time.Duration(windowValue.Duration), Bucket: time.Duration(windowValue.Bucket), Limit: pricing.MicroUSD(windowValue.LimitMicroUSD)})
		}
		if err := policy.Validate(value.Limits.MaxBudgetBucketsPerWindow); err != nil {
			return nil, fmt.Errorf("budget policy %q: %w", policy.ID, err)
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func endpointFamily(value string) provider.Family {
	if value == "azure_openai_responses" {
		return provider.FamilyOpenAIResponses
	}
	if value == "bedrock_anthropic_messages" {
		return provider.FamilyBedrockMessages
	}
	return provider.Family(value)
}

// routingCapabilities narrows the provider contract to the capability fields
// consumed by route planning. Provider-specific adapter features remain on the
// provider profile and are not silently widened into routing behavior.
func routingCapabilities(value provider.CapabilitySet) routing.CapabilitySet {
	result := routing.CapabilitySet{Version: value.Version, Features: make(map[routing.Feature]routing.Capability)}
	for source, target := range map[provider.Feature]routing.Feature{
		provider.FeatureText:             routing.FeatureText,
		provider.FeatureToolCall:         routing.FeatureToolCall,
		provider.FeatureStructuredOutput: routing.FeatureStructuredOutput,
		provider.FeatureReasoning:        routing.FeatureReasoning,
		provider.FeatureContinuation:     routing.FeatureContinuation,
	} {
		if capability, ok := value.Features[source]; ok {
			result.Features[target] = routing.Capability{State: routing.CapabilityState(capability.State), Transform: capability.Transform, Reason: capability.Reason}
		}
	}
	return result
}
