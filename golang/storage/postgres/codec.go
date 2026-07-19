package postgres

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

// EncodeUSD emits the canonical fixed-scale decimal accepted by NUMERIC(38,18).
// It deliberately returns text rather than a float or a driver-specific
// numeric value so callers can bind it as a positional parameter without a
// lossy conversion.
func EncodeUSD(value pricing.USD) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	return value.String(), nil
}

// DecodeUSD accepts only a decimal string. Exponents, signs, NaN, infinity,
// whitespace and values requiring rounding are rejected by ParseUSD.
func DecodeUSD(value string) (pricing.USD, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "eE+") {
		return pricing.USD{}, fmt.Errorf("PostgreSQL USD value must be an exact decimal")
	}
	parsed, err := pricing.ParseUSD(value)
	if err != nil {
		return pricing.USD{}, fmt.Errorf("decode PostgreSQL USD: %w", err)
	}
	return parsed, nil
}

// DecodeUSDValue handles the text/byte forms returned by pgx's default
// numeric codec while keeping all validation in DecodeUSD.
func DecodeUSDValue(value any) (pricing.USD, error) {
	switch typed := value.(type) {
	case string:
		return DecodeUSD(typed)
	case []byte:
		return DecodeUSD(string(typed))
	default:
		return pricing.USD{}, fmt.Errorf("decode PostgreSQL USD from %T: expected text", value)
	}
}

func EncodeNullableUSD(value *pricing.USD) (*string, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := EncodeUSD(*value)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func DecodeNullableUSD(value *string) (*pricing.USD, error) {
	if value == nil {
		return nil, nil
	}
	decoded, err := DecodeUSD(*value)
	if err != nil {
		return nil, err
	}
	return &decoded, nil
}

// NumericBounds documents the exact PostgreSQL NUMERIC(38,18) limits and is
// used by schema/codec tests without introducing a machine-sized conversion.
func NumericBounds() (min, max *big.Int) {
	min = new(big.Int)
	max = new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)
	max.Sub(max, big.NewInt(1))
	return min, max
}
