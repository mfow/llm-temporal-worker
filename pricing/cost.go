package pricing

import "fmt"

func CostFromUsage(entry Entry, usage Usage) (Cost, error) {
	components := []struct {
		price DecimalUSD
		units int64
		name  string
	}{
		{entry.Prices.InputPerMillion, usage.InputTokens, "input"},
		{entry.Prices.OutputPerMillion, usage.OutputTokens, "output"},
		{entry.Prices.CacheReadPerMillion, usage.CacheReadTokens, "cache_read"},
		{entry.Prices.CacheWritePerMillion, usage.CacheWriteTokens, "cache_write"},
		{entry.Prices.ReasoningPerMillion, usage.ReasoningTokens, "reasoning"},
		{entry.Prices.PerRequest, 1, "per_request"},
	}
	total := MicroUSD(0)
	for _, component := range components {
		if component.units < 0 {
			return Cost{}, fmt.Errorf("usage %s is negative", component.name)
		}
		value, err := CeilMicroUSD(component.price, component.units, 1_000_000)
		if err != nil {
			return Cost{}, fmt.Errorf("usage %s: %w", component.name, err)
		}
		total, err = total.Add(value)
		if err != nil {
			return Cost{}, err
		}
	}
	return Cost{MicroUSD: total, Currency: entry.Currency, Method: CostCatalogUsage, CatalogVersion: entry.Version}, nil
}
