package pricing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// DecimalUSD is a non-negative exact decimal configuration price. It keeps
// the numerator and scale rather than a float, so all billing decisions remain
// deterministic across architectures.
type DecimalUSD struct {
	numerator big.Int
	scale     int
	raw       string
}

func ParseDecimalUSD(value string) (DecimalUSD, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return DecimalUSD{}, fmt.Errorf("decimal price must be a non-negative ASCII decimal")
	}
	if strings.Count(value, ".") > 1 || value == "." {
		return DecimalUSD{}, fmt.Errorf("decimal price %q is invalid", value)
	}
	parts := strings.SplitN(value, ".", 2)
	whole, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" || (fraction == "" && len(parts) == 2) {
		return DecimalUSD{}, fmt.Errorf("decimal price %q is invalid", value)
	}
	for _, character := range whole + fraction {
		if character < '0' || character > '9' {
			return DecimalUSD{}, fmt.Errorf("decimal price %q contains a non-digit", value)
		}
	}
	numerator := new(big.Int)
	if _, ok := numerator.SetString(whole+fraction, 10); !ok {
		return DecimalUSD{}, fmt.Errorf("decimal price %q cannot be parsed", value)
	}
	return DecimalUSD{numerator: *numerator, scale: len(fraction), raw: value}, nil
}

func MustDecimalUSD(value string) DecimalUSD {
	parsed, err := ParseDecimalUSD(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (decimal DecimalUSD) String() string {
	if decimal.raw != "" {
		return decimal.raw
	}
	digits := decimal.numerator.String()
	if decimal.scale == 0 {
		return digits
	}
	if len(digits) <= decimal.scale {
		digits = strings.Repeat("0", decimal.scale-len(digits)+1) + digits
	}
	position := len(digits) - decimal.scale
	return digits[:position] + "." + digits[position:]
}

func (decimal DecimalUSD) MarshalJSON() ([]byte, error) {
	if err := decimal.valid(); err != nil {
		return nil, err
	}
	return json.Marshal(decimal.String())
}

func (decimal DecimalUSD) valid() error {
	if decimal.scale < 0 || decimal.scale > 18 || decimal.numerator.Sign() < 0 {
		return fmt.Errorf("decimal price is invalid")
	}
	return nil
}

// CeilMicroUSD computes ceil(priceUSD * units / unitsPerPrice * 1e6).
func CeilMicroUSD(price DecimalUSD, units, unitsPerPrice int64) (MicroUSD, error) {
	if err := price.valid(); err != nil {
		return 0, err
	}
	if units < 0 || unitsPerPrice <= 0 {
		return 0, fmt.Errorf("units must be non-negative and units_per_price positive")
	}
	numerator := new(big.Int).Set(&price.numerator)
	numerator.Mul(numerator, big.NewInt(units))
	numerator.Mul(numerator, big.NewInt(1_000_000))
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(price.scale)), nil)
	denominator.Mul(denominator, big.NewInt(unitsPerPrice))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("microUSD result overflows int64")
	}
	result := MicroUSD(quotient.Int64())
	if !result.Valid() {
		return 0, fmt.Errorf("microUSD result exceeds Redis-safe range")
	}
	return result, nil
}
