package state

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const handleVersion = "ctn_v1"

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Key is one HMAC key. The secret is copied at construction time.
type Key struct {
	ID      string
	Secret  []byte
	Primary bool
}

// Keyring signs and verifies opaque continuation handles. It is safe for
// concurrent use after construction because the key material is immutable.
type Keyring struct {
	keys   map[string][]byte
	order  []string
	reader io.Reader
}

func NewKeyring(keys []Key, reader io.Reader) (*Keyring, error) {
	if reader == nil {
		reader = rand.Reader
	}
	result := &Keyring{keys: make(map[string][]byte), reader: reader}
	primary := ""
	for _, key := range keys {
		if !keyIDPattern.MatchString(key.ID) || len(key.Secret) < 32 {
			return nil, fmt.Errorf("invalid continuation key %q", key.ID)
		}
		if _, exists := result.keys[key.ID]; exists {
			return nil, fmt.Errorf("duplicate continuation key %q", key.ID)
		}
		if key.Primary {
			if primary != "" {
				return nil, fmt.Errorf("multiple primary continuation keys")
			}
			primary = key.ID
		}
		result.keys[key.ID] = append([]byte(nil), key.Secret...)
		result.order = append(result.order, key.ID)
	}
	if primary == "" {
		if len(result.order) != 1 {
			return nil, fmt.Errorf("exactly one primary continuation key is required")
		}
		primary = result.order[0]
	}
	// Keep the primary ID first so a key rotation remains deterministic.
	if primary != result.order[0] {
		for index, id := range result.order {
			if id == primary {
				result.order = append([]string{id}, append(result.order[:index], result.order[index+1:]...)...)
				break
			}
		}
	}
	return result, nil
}

func (keyring *Keyring) Issue(tenant string) (string, error) {
	if keyring == nil || tenant == "" {
		return "", ErrInvalidHandle
	}
	keyID := keyring.order[0]
	randomID := make([]byte, 16)
	if _, err := io.ReadFull(keyring.reader, randomID); err != nil {
		return "", fmt.Errorf("generate continuation handle: %w", err)
	}
	encodedID := base64.RawURLEncoding.EncodeToString(randomID)
	mac := keyring.mac(keyID, randomID, tenant)
	return strings.Join([]string{handleVersion, keyID, encodedID, base64.RawURLEncoding.EncodeToString(mac)}, "."), nil
}

// Verify validates a handle and returns its random identifier. The result is
// deliberately not a provider or transcript identifier.
func (keyring *Keyring) Verify(tenant, handle string) ([]byte, error) {
	if keyring == nil || tenant == "" || len(handle) > 512 {
		return nil, ErrInvalidHandle
	}
	parts := strings.Split(handle, ".")
	if len(parts) != 4 || parts[0] != handleVersion || !keyIDPattern.MatchString(parts[1]) {
		return nil, ErrInvalidHandle
	}
	secret, ok := keyring.keys[parts[1]]
	if !ok {
		return nil, ErrInvalidHandle
	}
	randomID, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(randomID) != 16 {
		return nil, ErrInvalidHandle
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(provided) != sha256.Size {
		return nil, ErrInvalidHandle
	}
	expected := keyring.macWith(secret, parts[1], randomID, tenant)
	if subtle.ConstantTimeCompare(provided, expected) != 1 || !hmac.Equal(provided, expected) {
		return nil, ErrInvalidHandle
	}
	return append([]byte(nil), randomID...), nil
}

func (keyring *Keyring) mac(keyID string, randomID []byte, tenant string) []byte {
	return keyring.macWith(keyring.keys[keyID], keyID, randomID, tenant)
}

func (keyring *Keyring) macWith(secret []byte, keyID string, randomID []byte, tenant string) []byte {
	hash := hmac.New(sha256.New, secret)
	writeField := func(value []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		hash.Write(length[:])
		hash.Write(value)
	}
	writeField([]byte(handleVersion))
	writeField([]byte(keyID))
	writeField(randomID)
	writeField([]byte(tenant))
	return hash.Sum(nil)
}
