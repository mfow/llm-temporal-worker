package openaichat

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	openai "github.com/openai/openai-go/v3"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

func rawResponseObject(response *openai.ChatCompletion) (map[string]json.RawMessage, error) {
	if response == nil || strings.TrimSpace(response.RawJSON()) == "" {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response.RawJSON()), &fields); err != nil {
		return nil, fmt.Errorf("provider response metadata: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("provider response metadata is not an object")
	}
	return fields, nil
}

func addRawFact(target map[string]json.RawMessage, key string, value json.RawMessage) {
	if target == nil || key == "" || !json.Valid(value) {
		return
	}
	target[key] = append(json.RawMessage(nil), value...)
}

func parseDecimalUSD(raw json.RawMessage, field string) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, fmt.Errorf("%s is missing", field)
	}
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "\"") {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, fmt.Errorf("%s is not a decimal", field)
		}
		value = strings.TrimSpace(decoded)
	}
	rat, err := decimalRat(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	if rat.Sign() < 0 {
		return 0, fmt.Errorf("%s must not be negative", field)
	}
	scaled := new(big.Rat).Mul(rat, big.NewRat(1_000_000, 1))
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("%s exceeds microUSD range", field)
	}
	return quotient.Int64(), nil
}

func decimalRat(value string) (*big.Rat, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("is empty")
	}
	sign := 1
	if value[0] == '+' || value[0] == '-' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	if value == "" {
		return nil, fmt.Errorf("has no digits")
	}
	exponent := 0
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		exponentValue := value[index+1:]
		if exponentValue == "" {
			return nil, fmt.Errorf("has an invalid exponent")
		}
		parsed, ok := new(big.Int).SetString(exponentValue, 10)
		if !ok || !parsed.IsInt64() {
			return nil, fmt.Errorf("has an invalid exponent")
		}
		exponent = int(parsed.Int64())
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("has more than one decimal point")
	}
	whole, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" {
		whole = "0"
	}
	digits := whole + fraction
	for _, character := range digits {
		if character < '0' || character > '9' {
			return nil, fmt.Errorf("is not a decimal")
		}
	}
	numerator, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("is not a decimal")
	}
	if sign < 0 {
		numerator.Neg(numerator)
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
	if exponent > 0 {
		numerator.Mul(numerator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
	} else if exponent < 0 {
		denominator.Mul(denominator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil))
	}
	return new(big.Rat).SetFrac(numerator, denominator), nil
}

func responseAugmentCost(response *llm.Response, raw json.RawMessage, field string, method string) error {
	actual, err := parseDecimalUSD(raw, field)
	if err != nil {
		return err
	}
	response.Cost = llm.Cost{Currency: "USD", ActualMicroUSD: actual, Method: method}
	addRawFact(response.Provider.Raw, field, raw)
	addRawFact(response.Usage.ProviderRaw, field, raw)
	return nil
}
