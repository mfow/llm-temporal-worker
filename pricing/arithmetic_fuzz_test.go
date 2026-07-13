package pricing

import "testing"

func FuzzParseDecimalAndCeil(f *testing.F) {
	f.Add("0.000001", int64(1))
	f.Add("12.34", int64(100))
	f.Fuzz(func(t *testing.T, value string, units int64) {
		price, err := ParseDecimalUSD(value)
		if err != nil || units < 0 {
			return
		}
		_, _ = CeilMicroUSD(price, units, 1_000_000)
	})
}
