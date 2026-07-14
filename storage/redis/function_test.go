package redis

import (
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/admission"
)

func TestAdmissionWireUsesLuaFieldNames(t *testing.T) {
	attempt := admission.AttemptFacts{RouteID: "route", ProviderRequestID: "request", AttemptNumber: 3}
	attemptData, err := encodeAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	attemptJSON := string(attemptData)
	if !strings.Contains(attemptJSON, `"route_id":"route"`) || strings.Contains(attemptJSON, `"RouteID"`) {
		t.Fatalf("attempt wire uses Go field names: %s", attemptJSON)
	}
	decodedAttempt, err := decodeAttempt(attemptData)
	if err != nil || decodedAttempt != attempt {
		t.Fatalf("attempt round trip = %#v, %v", decodedAttempt, err)
	}

	outcomeData, err := encodeOutcome(admission.AttemptOutcome{Certainty: admission.Rejected, Incurred: 7, Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	outcomeJSON := string(outcomeData)
	for _, field := range []string{`"certainty":"rejected"`, `"incurred":"7"`, `"attempt":{"route_id":"route"`} {
		if !strings.Contains(outcomeJSON, field) {
			t.Fatalf("outcome wire missing Lua field %q: %s", field, outcomeJSON)
		}
	}
	decodedOutcome, err := decodeOutcome(outcomeData)
	if err != nil || decodedOutcome.Certainty != admission.Rejected || decodedOutcome.Incurred != 7 || decodedOutcome.Attempt != attempt {
		t.Fatalf("outcome round trip = %#v, %v", decodedOutcome, err)
	}
}

func TestAdmissionFunctionMetadataIsStableAndVersioned(t *testing.T) {
	metadata := AdmissionFunctionMetadata()
	if metadata.Library != AdmissionFunctionLibrary || metadata.Version != AdmissionFunctionVersion {
		t.Fatalf("unexpected function metadata %#v", metadata)
	}
	source := AdmissionFunctionSource()
	if !strings.Contains(source, "ACTION == 'begin'") || !strings.Contains(source, "ACTION == 'continue'") || !strings.Contains(source, "ACTION == 'complete'") || !strings.Contains(source, "ACTION == 'fail'") {
		t.Fatal("admission function is missing a required transition")
	}
	if len(AdmissionFunctionDigest()) != 64 || AdmissionFunctionDigest() == "" {
		t.Fatalf("invalid function digest %q", AdmissionFunctionDigest())
	}
	if !strings.Contains(source, "redis.call('TIME')") {
		t.Fatal("admission function does not use Redis server time")
	}
	if !strings.Contains(source, "can_increment_reservations") || !strings.Contains(source, "redis.call('TTL'") {
		t.Fatal("admission function lacks mutation preflight or monotonic bucket TTL")
	}
	if strings.Contains(source, ".. KEYS") || strings.Contains(source, "..ARGV") {
		t.Fatal("function dynamically interpolates key names")
	}
}

func TestAdmissionFunctionPreservesRecordRetentionOnUpdates(t *testing.T) {
	source := AdmissionFunctionSource()
	for _, fragment := range []string{
		"local current_ttl = redis.call('TTL', key)",
		"current_ttl == -2",
		"current_ttl >= 0 and current_ttl < ttl_value",
		"redis.call('EXPIRE', key, tostring(restore_ttl))",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("admission function does not preserve record TTL: missing %q", fragment)
		}
	}
}

func TestContinuationFunctionUsesCreateIfAbsentAndTTL(t *testing.T) {
	if !strings.Contains(continuationFunctionSource, "'NX'") || !strings.Contains(continuationFunctionSource, "EXPIRE") {
		t.Fatal("continuation function is not immutable/expiring")
	}
	if !strings.Contains(continuationFunctionSource, "#KEYS >= 3") {
		t.Fatal("continuation function does not support two-key root writes")
	}
	if !strings.Contains(continuationFunctionSource, "DEL', KEYS[1], KEYS[2]") {
		t.Fatal("continuation function does not clean up provisional conflicts")
	}
}
