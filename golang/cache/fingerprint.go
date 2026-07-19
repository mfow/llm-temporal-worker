package cache

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const fingerprintDomain = "llm-temporal-worker/exact-response-cache/v1\x00"

// Fingerprint is a fixed-size, keyed identity. Raw request content is never
// returned or logged by this package.
type Fingerprint [32]byte

func (fingerprint Fingerprint) Hex() string { return hex.EncodeToString(fingerprint[:]) }

// Compute HMACs the canonical semantic manifest. A non-empty, deployment-
// scoped secret is required so leaked database keys cannot be used to confirm
// guesses about prompt content.
func Compute(key []byte, input Input) (Fingerprint, error) {
	if len(key) == 0 {
		return Fingerprint{}, fmt.Errorf("cache fingerprint key is required")
	}
	canonical, err := input.Canonical()
	if err != nil {
		return Fingerprint{}, err
	}
	hasher := hmac.New(sha256.New, key)
	_, _ = hasher.Write([]byte(fingerprintDomain))
	_, _ = hasher.Write(canonical)
	var result Fingerprint
	copy(result[:], hasher.Sum(nil))
	return result, nil
}
