package redis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
)

func TestFormatRedisTimeDistinguishesLegacyZeroFromUnixEpoch(t *testing.T) {
	tests := []struct {
		name  string
		value time.Time
		want  string
	}{
		{name: "legacy zero", value: time.Time{}, want: "0:0"},
		{name: "unix epoch", value: time.Unix(0, 0).UTC(), want: "+0:0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatRedisTime(test.value); got != test.want {
				t.Fatalf("formatRedisTime(%s) = %q, want %q", test.value, got, test.want)
			}

			got, err := parseRedisTime(test.want)
			if err != nil {
				t.Fatalf("parseRedisTime(%q): %v", test.want, err)
			}
			if !reflect.DeepEqual(got, test.value) {
				t.Fatalf("parseRedisTime(%q) = %#v, want %#v", test.want, got, test.value)
			}
		})
	}
}

func TestOperationCodecRoundTripPreservesUnixEpoch(t *testing.T) {
	encodedInput, err := json.Marshal(operationWire{
		Schema:       operationSchema,
		ID:           "operation-epoch",
		ScopeKey:     "tenant-epoch",
		Digest:       strings.Repeat("0", 64),
		State:        admission.StateReserved,
		Reserved:     "0",
		Incurred:     "0",
		Final:        "0",
		Reservations: []reservationWire{},
		Token:        "dispatch-token",
		Lease:        time.Unix(1, 0).UTC(),
		Created:      "0:0",
		Updated:      "00:0",
		Expires:      time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := decodeOperation(encodedInput)
	if err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if got, want := first.UpdatedAt, time.Unix(0, 0).UTC(); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded updated time = %#v, want %#v", got, want)
	}

	encoded, err := encodeOperation(first)
	if err != nil {
		t.Fatalf("encode decoded operation: %v", err)
	}
	second, err := decodeOperation(encoded)
	if err != nil {
		t.Fatalf("decode re-encoded operation: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("operation codec changed semantic value:\nfirst=%#v\nsecond=%#v", first, second)
	}
}
