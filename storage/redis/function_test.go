package redis

import (
	"strings"
	"testing"
)

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

func TestContinuationFunctionUsesCreateIfAbsentAndTTL(t *testing.T) {
	if !strings.Contains(continuationFunctionSource, "'NX'") || !strings.Contains(continuationFunctionSource, "EXPIRE") {
		t.Fatal("continuation function is not immutable/expiring")
	}
	if !strings.Contains(continuationFunctionSource, "DEL', KEYS[1], KEYS[2]") {
		t.Fatal("continuation function does not clean up provisional conflicts")
	}
}
