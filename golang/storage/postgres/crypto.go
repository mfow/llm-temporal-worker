package postgres

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const (
	keyDigestBytes = 32
	nonceBytes     = 12
	maxEnvelope    = 16 << 20
)

// Keyring contains versioned 32-byte keys. During rotation the old key stays
// in Keys until all values encrypted with it have been rewritten. The active
// key is used for new values and is intentionally stored separately from
// ciphertext so operators can rotate without changing database columns.
type Keyring struct {
	Active string
	Keys   map[string][]byte
	Random io.Reader
}

func (ring Keyring) key(id string) ([]byte, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("encryption key id is empty")
	}
	key, ok := ring.Keys[id]
	if !ok || len(key) != keyDigestBytes {
		return nil, fmt.Errorf("encryption key %q is unavailable", id)
	}
	return key, nil
}

func (ring Keyring) activeKey() (string, []byte, error) {
	key, err := ring.key(ring.Active)
	if err != nil {
		return "", nil, err
	}
	return ring.Active, key, nil
}

// ScopeHMAC derives a fixed-size tenant/project lookup value. The raw value
// is never returned to callers after this function and is safe to persist in
// the keyed lookup columns described by the PostgreSQL architecture.
func ScopeHMAC(key []byte, value string) ([keyDigestBytes]byte, error) {
	var digest [keyDigestBytes]byte
	if len(key) != keyDigestBytes {
		return digest, errors.New("scope HMAC key must be exactly 32 bytes")
	}
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return digest, errors.New("scope value is empty or contains control characters")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("llmtw/scope/v1\x00"))
	_, _ = mac.Write([]byte(value))
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

// UUIDv7 returns an application identifier with an injectable random/time
// source in tests. uuid.NewV7 uses the system source and produces RFC 9562
// UUIDv7 values suitable for the database primary keys.
func UUIDv7() (uuid.UUID, error) { return uuid.NewV7() }

// EnvelopeContext is authenticated as AEAD additional data. Binding scope,
// operation, payload kind and digest prevents a ciphertext copied between
// tenants or payload columns from being accepted.
type EnvelopeContext struct {
	ScopeID     uuid.UUID
	OperationID uuid.UUID
	PayloadKind string
	Digest      [keyDigestBytes]byte
}

func (context EnvelopeContext) bytes() ([]byte, error) {
	if context.ScopeID == uuid.Nil {
		return nil, errors.New("envelope scope id is nil")
	}
	if context.PayloadKind == "" || strings.ContainsAny(context.PayloadKind, "\x00\r\n") {
		return nil, errors.New("envelope payload kind is empty or contains control characters")
	}
	buf := make([]byte, 0, 64+len(context.PayloadKind))
	buf = append(buf, []byte("llmtw/envelope/v1\x00")...)
	buf = append(buf, context.ScopeID[:]...)
	buf = append(buf, context.OperationID[:]...)
	buf = append(buf, []byte(context.PayloadKind)...)
	buf = append(buf, 0)
	buf = append(buf, context.Digest[:]...)
	return buf, nil
}

func contextDigest(context EnvelopeContext) ([keyDigestBytes]byte, error) {
	var digest [keyDigestBytes]byte
	bytes, err := context.bytes()
	if err != nil {
		return digest, err
	}
	return sha256.Sum256(bytes), nil
}

// SealedValue is the database representation of one encrypted locator or
// provider reference. Ciphertext includes the random GCM nonce; KeyID and
// ContextDigest are stored in their corresponding columns.
type SealedValue struct {
	KeyID       string
	Ciphertext  []byte
	ContextHash [keyDigestBytes]byte
}

func (ring Keyring) Seal(context EnvelopeContext, plaintext []byte) (SealedValue, error) {
	var sealed SealedValue
	if len(plaintext) > maxEnvelope {
		return sealed, fmt.Errorf("envelope plaintext exceeds %d bytes", maxEnvelope)
	}
	keyID, key, err := ring.activeKey()
	if err != nil {
		return sealed, err
	}
	additionalData, err := context.bytes()
	if err != nil {
		return sealed, err
	}
	sealed.ContextHash, err = contextDigest(context)
	if err != nil {
		return sealed, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return sealed, fmt.Errorf("create envelope cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return sealed, fmt.Errorf("create envelope AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	random := ring.Random
	if random == nil {
		random = rand.Reader
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return sealed, fmt.Errorf("generate envelope nonce: %w", err)
	}
	sealed.KeyID = keyID
	sealed.Ciphertext = gcm.Seal(nonce, nonce, plaintext, additionalData)
	return sealed, nil
}

func (ring Keyring) Open(context EnvelopeContext, sealed SealedValue) ([]byte, error) {
	if len(sealed.Ciphertext) < nonceBytes {
		return nil, errors.New("envelope ciphertext is truncated")
	}
	key, err := ring.key(sealed.KeyID)
	if err != nil {
		return nil, err
	}
	additionalData, err := context.bytes()
	if err != nil {
		return nil, err
	}
	digest, err := contextDigest(context)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(digest[:], sealed.ContextHash[:]) {
		return nil, errors.New("envelope context digest mismatch")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create envelope cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create envelope AEAD: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(sealed.Ciphertext) < nonceSize+gcm.Overhead() {
		return nil, errors.New("envelope ciphertext is truncated")
	}
	plaintext, err := gcm.Open(nil, sealed.Ciphertext[:nonceSize], sealed.Ciphertext[nonceSize:], additionalData)
	if err != nil {
		return nil, errors.New("decrypt envelope: authentication failed")
	}
	return plaintext, nil
}

// EncodeVersionedKey is used by callers that need an unambiguous key-id wire
// value in audit metadata. The fixed format avoids accidental concatenation of
// user-controlled strings.
func EncodeVersionedKey(version string, digest [keyDigestBytes]byte) []byte {
	buf := make([]byte, 2+len(version)+keyDigestBytes)
	binary.BigEndian.PutUint16(buf[:2], uint16(len(version)))
	copy(buf[2:], version)
	copy(buf[2+len(version):], digest[:])
	return buf
}
