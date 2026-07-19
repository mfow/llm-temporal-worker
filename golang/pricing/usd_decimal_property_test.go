package pricing

import "testing"

func FuzzUSDParseRoundTrip(f *testing.F) {
	for _, seed := range []string{"0", "1", "10.000000000000000001", "99999999999999999999.999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		value, err := ParseUSD(input)
		if err != nil {
			return
		}
		roundTrip, err := ParseUSD(value.String())
		if err != nil {
			t.Fatalf("canonical value %q did not parse: %v", value.String(), err)
		}
		if value.Cmp(roundTrip) != 0 {
			t.Fatalf("USD round trip changed %q to %q", value.String(), roundTrip.String())
		}
	})
}
