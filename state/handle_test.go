package state

import (
	"bytes"
	"strings"
	"testing"
)

func TestHandleRoundTripAndTenantBinding(t *testing.T) {
	keyring, err := NewKeyring([]Key{{ID: "k1", Secret: bytes.Repeat([]byte{7}, 32), Primary: true}}, bytes.NewReader(bytes.Repeat([]byte{9}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := keyring.Issue("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(handle, "ctn_v1.k1.") {
		t.Fatalf("unexpected handle %q", handle)
	}
	if _, err := keyring.Verify("tenant-a", handle); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Verify("tenant-b", handle); err != ErrInvalidHandle {
		t.Fatalf("cross-tenant verification error = %v", err)
	}
}

func TestHandleRejectsTamperingAndRetiredKey(t *testing.T) {
	keyring, err := NewKeyring([]Key{{ID: "k1", Secret: bytes.Repeat([]byte{1}, 32), Primary: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := keyring.Issue("t")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(handle, ".")
	parts[2] = "A" + parts[2][1:]
	if _, err := keyring.Verify("t", strings.Join(parts, ".")); err != ErrInvalidHandle {
		t.Fatalf("tampered handle error = %v", err)
	}
	unknown := strings.Replace(handle, ".k1.", ".retired.", 1)
	if _, err := keyring.Verify("t", unknown); err != ErrInvalidHandle {
		t.Fatalf("retired key error = %v", err)
	}
}

func FuzzVerifyHandleNeverPanics(f *testing.F) {
	f.Add("ctn_v1.k1.AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	f.Fuzz(func(t *testing.T, value string) {
		keyring, err := NewKeyring([]Key{{ID: "k1", Secret: bytes.Repeat([]byte{1}, 32), Primary: true}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = keyring.Verify("tenant", value)
	})
}
