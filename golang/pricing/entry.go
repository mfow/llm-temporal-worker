package pricing

import "time"

type UnitPrices struct {
	InputPerMillion      DecimalUSD
	OutputPerMillion     DecimalUSD
	CacheReadPerMillion  DecimalUSD
	CacheWritePerMillion DecimalUSD
	ReasoningPerMillion  DecimalUSD
	// PerRequest is the absolute USD charge applied once per invocation. It
	// intentionally does not use the per-million token denominator.
	PerRequest DecimalUSD
}

type Entry struct {
	Provider       string
	Family         string
	EndpointID     string
	Region         string
	Model          string
	ProviderTier   string
	Prices         UnitPrices
	EffectiveFrom  time.Time
	EffectiveUntil time.Time
	Provenance     string
	Version        string
}

func (entry Entry) Active(now time.Time) bool {
	return (entry.EffectiveFrom.IsZero() || !now.Before(entry.EffectiveFrom)) && (entry.EffectiveUntil.IsZero() || now.Before(entry.EffectiveUntil))
}

type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	MediaUnits       int64
}

type CostMethod string

const (
	CostProviderReported    CostMethod = "provider_reported"
	CostCatalogUsage        CostMethod = "catalog_usage"
	CostReconstructedUsage  CostMethod = "reconstructed_usage"
	CostRetainedReservation CostMethod = "retained_reservation"
)

type Cost struct {
	// USD is the authoritative exact amount. MicroUSD is materialized only
	// when the engine crosses into the integer Redis admission boundary.
	USD            USD
	MicroUSD       MicroUSD
	Method         CostMethod
	CatalogVersion string
}
