package redis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/admission"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/state"
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

func TestContinuationCodecRoundTripDecodesMessageAndToolItems(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	transcript := []llm.Item{
		llm.Message{Actor: llm.ActorHuman, Content: []llm.Part{llm.TextPart{Text: "find the weather"}}},
		llm.ToolCall{ID: "call-weather", Name: "weather", Arguments: json.RawMessage(`{"city":"Sydney"}`)},
	}
	_, digest, err := state.CanonicalTranscript(transcript)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Continuation{
		ID:                 "continuation-handle",
		Tenant:             "tenant-a",
		ParentID:           "parent-continuation",
		Transcript:         transcript,
		TranscriptDigest:   digest,
		TranscriptComplete: true,
		ProviderState: []state.OpaqueStateRef{{
			Provider:      "openai",
			EndpointID:    "responses-us-east",
			AccountRegion: "us-east-1",
			Family:        "responses",
			ModelLineage:  "gpt-5",
			Media:         "application/octet-stream",
			Data:          []byte("provider-state"),
			Required:      true,
		}},
		Pinning: state.Pinning{
			Provider:      "openai",
			EndpointID:    "responses-us-east",
			AccountRegion: "us-east-1",
			Family:        "responses",
			ModelLineage:  "gpt-5",
		},
		LastOperationID:   "operation-42",
		CapabilityVersion: "cap-v1",
		PriceVersion:      "price-v1",
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		Depth:             2,
	}
	expected := want.Clone()
	encoded, err := encodeContinuation(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeContinuation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("continuation codec changed semantic value:\ngot=%#v\nwant=%#v", got, expected)
	}
	if _, ok := got.Transcript[0].(llm.Message); !ok {
		t.Fatalf("decoded first item type = %T, want llm.Message", got.Transcript[0])
	}
	if _, ok := got.Transcript[1].(llm.ToolCall); !ok {
		t.Fatalf("decoded second item type = %T, want llm.ToolCall", got.Transcript[1])
	}
}

func TestContinuationCodecRejectsMalformedItemUnion(t *testing.T) {
	encoded := []byte(`{"schema":"continuation/v1","value":{"Transcript":[{"kind":"unknown"}]}}`)
	if _, err := decodeContinuation(encoded); err == nil {
		t.Fatal("malformed continuation item union was accepted")
	}
}
