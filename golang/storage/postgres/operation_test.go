package postgres

import (
	"bytes"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

func TestNormalizeManifestRequiresObject(t *testing.T) {
	if got, err := normalizeManifest(nil); err != nil || string(got) != "{}" {
		t.Fatalf("empty manifest = %q, %v", got, err)
	}
	if _, err := normalizeManifest([]byte(`[1]`)); err == nil {
		t.Fatal("array manifest unexpectedly accepted")
	}
	got, err := normalizeManifest([]byte(`{"model":"gpt","temperature":0}`))
	if err != nil || !bytes.Contains(got, []byte(`"model"`)) {
		t.Fatalf("object manifest = %q, %v", got, err)
	}
}

func TestOperationUUIDAndHMACAreStable(t *testing.T) {
	first := operationUUID("legacy-operation")
	if first != operationUUID("legacy-operation") {
		t.Fatal("legacy operation UUID is not stable")
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	if operationHMAC(key, "operation-key", []byte("one")) != operationHMAC(key, "operation-key", []byte("one")) {
		t.Fatal("operation HMAC is not stable")
	}
	if operationHMAC(key, "operation-key", []byte("one")) == operationHMAC(key, "operation-key", []byte("two")) {
		t.Fatal("operation HMAC did not bind the value")
	}
}

func TestSafeReasonAndExactMoney(t *testing.T) {
	if got := safeReason("Provider timeout: request/123"); got != "providertimeoutrequest123" {
		t.Fatalf("safe reason = %q", got)
	}
	if _, err := EncodeUSD(pricing.MustUSD("1.000000000000000000")); err != nil {
		t.Fatalf("exact USD encoding failed: %v", err)
	}
}
