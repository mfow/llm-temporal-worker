package postgres

import (
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestUSDCodecRejectsLossyOrAmbiguousValues(t *testing.T) {
	for _, value := range []string{"1e-3", "1E3", "+1", "-1", " 1", "1 ", "NaN", "Infinity", "1.1234567890123456789"} {
		if _, err := DecodeUSD(value); err == nil {
			t.Errorf("ambiguous USD value %q accepted", value)
		}
	}
	for _, value := range []string{"0", "0.000000000000000001", "1", "10.123456789012345678", "99999999999999999999.999999999999999999"} {
		parsed, err := DecodeUSD(value)
		if err != nil {
			t.Fatalf("DecodeUSD(%q): %v", value, err)
		}
		encoded, err := EncodeUSD(parsed)
		if err != nil {
			t.Fatalf("EncodeUSD(%q): %v", value, err)
		}
		if encoded != parsed.String() || len(encoded) < 20 {
			t.Fatalf("encoded %q is not canonical fixed-scale USD", encoded)
		}
	}
	if _, err := DecodeUSD("100000000000000000000.000000000000000000"); err == nil {
		t.Fatal("NUMERIC(38,18) overflow accepted")
	}
}

func TestNullableUSDPreservesUnknownAsNull(t *testing.T) {
	var unknown *pricing.USD
	encoded, err := EncodeNullableUSD(unknown)
	if err != nil || encoded != nil {
		t.Fatalf("unknown encode = %v, %v", encoded, err)
	}
	decoded, err := DecodeNullableUSD(nil)
	if err != nil || decoded != nil {
		t.Fatalf("unknown decode = %v, %v", decoded, err)
	}
}
