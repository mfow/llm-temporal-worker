package pricing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

const (
	// USDScale is the fixed number of fractional digits in the public money
	// contract. It matches PostgreSQL NUMERIC(38,18).
	USDScale       = 18
	usdWholeDigits = 20
)

var usdScaleFactor = new(big.Int).Exp(big.NewInt(10), big.NewInt(USDScale), nil)

// USD is an exact, non-negative fixed-scale US-dollar amount. The value is
// stored as an integer number of 10^-18 dollars; no float or machine-sized
// integer conversion is involved. A zero value is known free, while an
// optional *USD in higher-level records represents an unknown amount.
//
// The representation is deliberately opaque so callers cannot accidentally
// construct a value outside NUMERIC(38,18). Use ParseUSD or MustUSD.
type USD struct {
	units *big.Int
}

func ParseUSD(value string) (USD, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return USD{}, fmt.Errorf("USD must be a non-negative decimal string")
	}
	if strings.Count(value, ".") > 1 || value == "." {
		return USD{}, fmt.Errorf("USD %q is invalid", value)
	}
	parts := strings.SplitN(value, ".", 2)
	whole, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" || (len(parts) == 2 && fraction == "") {
		return USD{}, fmt.Errorf("USD %q is invalid", value)
	}
	if len(fraction) > USDScale || len(whole) > usdWholeDigits {
		return USD{}, fmt.Errorf("USD %q exceeds NUMERIC(38,18)", value)
	}
	for _, character := range whole + fraction {
		if character < '0' || character > '9' {
			return USD{}, fmt.Errorf("USD %q contains a non-digit", value)
		}
	}
	if len(whole) == usdWholeDigits && strings.TrimLeft(whole, "0") != "" {
		// A 20-digit whole part is valid only if it remains within the maximum
		// representable NUMERIC(38,18) value. The scaled integer check below
		// handles the fractional boundary exactly.
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	digits += strings.Repeat("0", USDScale-len(fraction))
	units, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return USD{}, fmt.Errorf("USD %q cannot be parsed", value)
	}
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)
	max.Sub(max, big.NewInt(1))
	if units.Cmp(max) > 0 {
		return USD{}, fmt.Errorf("USD %q exceeds NUMERIC(38,18)", value)
	}
	return USD{units: units}, nil
}

func MustUSD(value string) USD {
	parsed, err := ParseUSD(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (usd USD) valid() error {
	if usd.units == nil {
		return nil
	}
	if usd.units.Sign() < 0 {
		return fmt.Errorf("USD value is invalid")
	}
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)
	max.Sub(max, big.NewInt(1))
	if usd.units.Cmp(max) > 0 {
		return fmt.Errorf("USD value exceeds NUMERIC(38,18)")
	}
	return nil
}

func (usd USD) IsZero() bool { return usd.units == nil || usd.units.Sign() == 0 }

func (usd USD) Validate() error { return usd.valid() }

func (usd USD) Cmp(other USD) int {
	left, right := usd.units, other.units
	if left == nil {
		left = new(big.Int)
	}
	if right == nil {
		right = new(big.Int)
	}
	return left.Cmp(right)
}

func (usd USD) String() string {
	if usd.units == nil {
		return "0.000000000000000000"
	}
	digits := usd.units.String()
	if len(digits) <= USDScale {
		digits = strings.Repeat("0", USDScale-len(digits)+1) + digits
	}
	position := len(digits) - USDScale
	return digits[:position] + "." + digits[position:]
}

func (usd USD) MarshalJSON() ([]byte, error) {
	if err := usd.valid(); err != nil {
		return nil, err
	}
	return json.Marshal(usd.String())
}

func (usd *USD) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("USD must be encoded as a decimal string: %w", err)
	}
	parsed, err := ParseUSD(value)
	if err != nil {
		return err
	}
	*usd = parsed
	return nil
}

func (usd *USD) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("USD must be encoded as a quoted decimal string")
	}
	parsed, err := ParseUSD(node.Value)
	if err != nil {
		return err
	}
	*usd = parsed
	return nil
}

func (usd USD) Add(other USD) (USD, error) {
	if err := usd.valid(); err != nil {
		return USD{}, err
	}
	if err := other.valid(); err != nil {
		return USD{}, err
	}
	left, right := new(big.Int), new(big.Int)
	if usd.units != nil {
		left.Set(usd.units)
	}
	if other.units != nil {
		right.Set(other.units)
	}
	left.Add(left, right)
	result := USD{units: left}
	if err := result.valid(); err != nil {
		return USD{}, err
	}
	return result, nil
}

func (usd USD) Sub(other USD) (USD, error) {
	if err := usd.valid(); err != nil {
		return USD{}, err
	}
	if err := other.valid(); err != nil {
		return USD{}, err
	}
	if usd.Cmp(other) < 0 {
		return USD{}, fmt.Errorf("USD subtraction would be negative")
	}
	result := new(big.Int)
	if usd.units != nil {
		result.Set(usd.units)
	}
	if other.units != nil {
		result.Sub(result, other.units)
	}
	return USD{units: result}, nil
}

func (usd USD) SubOrZero(other USD) USD {
	result, err := usd.Sub(other)
	if err != nil {
		return USD{}
	}
	return result
}

// MulRatio multiplies an amount by a non-negative rational and rounds up to
// the fixed 18-digit scale. It is used for conservative safety ratios.
func (usd USD) MulRatio(numerator, denominator *big.Int) (USD, error) {
	if err := usd.valid(); err != nil {
		return USD{}, err
	}
	if numerator == nil || denominator == nil || numerator.Sign() < 0 || denominator.Sign() <= 0 {
		return USD{}, fmt.Errorf("USD ratio must be non-negative with a positive denominator")
	}
	value := new(big.Int)
	if usd.units != nil {
		value.Set(usd.units)
	}
	value.Mul(value, numerator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value, denominator, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	result := USD{units: quotient}
	if err := result.valid(); err != nil {
		return USD{}, err
	}
	return result, nil
}

// CeilUSD computes ceil(priceUSD * units / unitsPerPrice) at 18 fractional
// digits. Unlike CeilMicroUSD it preserves sub-micro-dollar values.
func CeilUSD(price DecimalUSD, units, unitsPerPrice int64) (USD, error) {
	if err := price.valid(); err != nil {
		return USD{}, err
	}
	if units < 0 || unitsPerPrice <= 0 {
		return USD{}, fmt.Errorf("units must be non-negative and units_per_price positive")
	}
	numerator := new(big.Int).Set(&price.numerator)
	numerator.Mul(numerator, big.NewInt(units))
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(price.scale)), nil)
	denominator.Mul(denominator, big.NewInt(unitsPerPrice))
	// Convert the exact rational to the fixed USD scale, rounding up only at
	// the final boundary.
	numerator.Mul(numerator, usdScaleFactor)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	result := USD{units: quotient}
	if err := result.valid(); err != nil {
		return USD{}, err
	}
	return result, nil
}

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
