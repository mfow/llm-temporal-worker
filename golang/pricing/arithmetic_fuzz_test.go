package pricing

import (
	"math/big"
	"testing"
)

func FuzzParseDecimalAndCeil(f *testing.F) {
	f.Add("0.000001", int64(1))
	f.Add("0.0000011", int64(1))
	f.Add("12.34", int64(100))
	f.Fuzz(func(t *testing.T, value string, units int64) {
		if len(value) > 64 || units < 0 || units > 1_000_000_000 {
			return
		}
		price, err := ParseDecimalUSD(value)
		if err != nil || price.scale > 18 {
			return
		}
		want := decimalCeilingOracle(t, value, units, 1_000_000)
		got, err := CeilMicroUSD(price, units, 1_000_000)
		if want.Cmp(big.NewInt(int64(RedisSafeLimit))) > 0 {
			if err == nil {
				t.Fatalf("overflow result = %d, want error", got)
			}
			return
		}
		if err != nil || got.Int64() != want.Int64() {
			t.Fatalf("CeilMicroUSD(%q, %d) = %d, %v; want %d", value, units, got, err, want)
		}
	})
}
