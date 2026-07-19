package postgres

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func testKeyring() Keyring {
	return Keyring{
		Active: "k1",
		Keys: map[string][]byte{
			"k1": bytes.Repeat([]byte{1}, keyDigestBytes),
			"k2": bytes.Repeat([]byte{2}, keyDigestBytes),
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{7}, 128)),
	}
}

func testEnvelopeContext() EnvelopeContext {
	return EnvelopeContext{
		ScopeID:     uuid.MustParse("018f0b0e-4d5c-7d4a-8c3b-6e7f8a9b0c1d"),
		OperationID: uuid.MustParse("018f0b0e-4d5c-7d4a-8c3b-6e7f8a9b0c1e"),
		PayloadKind: "request",
		Digest:      [keyDigestBytes]byte{1, 2, 3},
	}
}

func TestEnvelopeBindsScopeOperationKindAndDigest(t *testing.T) {
	ring := testKeyring()
	context := testEnvelopeContext()
	sealed, err := ring.Seal(context, []byte("opaque locator"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := ring.Open(context, sealed)
	if err != nil || string(opened) != "opaque locator" {
		t.Fatalf("open = %q, %v", opened, err)
	}
	for name, changed := range map[string]EnvelopeContext{
		"scope":     func() EnvelopeContext { value := context; value.ScopeID = uuid.New(); return value }(),
		"operation": func() EnvelopeContext { value := context; value.OperationID = uuid.New(); return value }(),
		"kind":      func() EnvelopeContext { value := context; value.PayloadKind = "result"; return value }(),
		"digest":    func() EnvelopeContext { value := context; value.Digest[0]++; return value }(),
	} {
		if _, err := ring.Open(changed, sealed); err == nil {
			t.Errorf("wrong %s context decrypted successfully", name)
		}
	}
	tampered := sealed
	tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	tampered.Ciphertext[len(tampered.Ciphertext)-1]++
	if _, err := ring.Open(context, tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted successfully")
	}
}

func TestEnvelopeRotationRetainsOldReadKey(t *testing.T) {
	context := testEnvelopeContext()
	oldRing := testKeyring()
	sealed, err := oldRing.Seal(context, []byte("old locator"))
	if err != nil {
		t.Fatal(err)
	}
	rotated := oldRing
	rotated.Active = "k2"
	if opened, err := rotated.Open(context, sealed); err != nil || string(opened) != "old locator" {
		t.Fatalf("rotated keyring cannot read old value: %q, %v", opened, err)
	}
	newSealed, err := rotated.Seal(context, []byte("new locator"))
	if err != nil {
		t.Fatal(err)
	}
	if newSealed.KeyID != "k2" {
		t.Fatalf("new value key id = %q, want k2", newSealed.KeyID)
	}
	if _, err := (Keyring{Active: "k2", Keys: map[string][]byte{"k2": bytes.Repeat([]byte{2}, keyDigestBytes)}}).Open(context, sealed); err == nil {
		t.Fatal("value decrypted after removing old key")
	}
}

func TestScopeHMACIsKeyedAndBounded(t *testing.T) {
	key := bytes.Repeat([]byte{3}, keyDigestBytes)
	one, err := ScopeHMAC(key, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := ScopeHMAC(bytes.Repeat([]byte{4}, keyDigestBytes), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if one == other {
		t.Fatal("different keys produced the same scope HMAC")
	}
	for _, value := range []string{"", "tenant\n", "tenant\x00"} {
		if _, err := ScopeHMAC(key, value); err == nil {
			t.Errorf("invalid scope value %q accepted", value)
		}
	}
}
