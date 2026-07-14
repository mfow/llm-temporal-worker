package state

import (
	"bytes"
	"testing"
)

func TestContinuationHandleTenantBindingInvariants(t *testing.T) {
	keyring, err := NewKeyring([]Key{{ID: "primary", Secret: bytes.Repeat([]byte{7}, 32), Primary: true}}, bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant-\u03b4"} {
		handle, err := keyring.Issue(tenant)
		if err != nil {
			t.Fatalf("Issue(%q): %v", tenant, err)
		}
		identifier, err := keyring.Verify(tenant, handle)
		if err != nil || len(identifier) != 16 {
			t.Fatalf("Verify(%q) = %x, %v", tenant, identifier, err)
		}
		if _, err := keyring.Verify(tenant+"-other", handle); err != ErrInvalidHandle {
			t.Fatalf("cross-tenant Verify(%q) error = %v, want ErrInvalidHandle", tenant, err)
		}
	}
}
