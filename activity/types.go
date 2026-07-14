package activity

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/llm"
)

const (
	APIVersion           = "llm.temporal/v1"
	GenerateActivityName = "llm.generate.v1"
	DefaultInlineBytes   = 256 * 1024
)

// GenerateRequest is the Temporal boundary wrapper around the canonical
// provider-neutral request. SDK clients, credentials, and provider wire types
// are intentionally not representable here.
type GenerateRequest struct {
	APIVersion string      `json:"api_version"`
	Request    llm.Request `json:"request"`
}

// GenerateResponse is the durable result returned by one inference Activity.
type GenerateResponse struct {
	APIVersion string         `json:"api_version"`
	Response   llm.Response   `json:"response"`
	Metadata   ResultMetadata `json:"metadata"`
}

type ResultMetadata struct {
	OperationID string `json:"operation_id,omitempty"`
}

// BlobRef is the Activity-level reference for content that does not belong in
// Temporal history. Locator values are opaque to this package and are never
// fetched by the Activity payload codec.
type BlobRef struct {
	Store      string     `json:"store"`
	Locator    string     `json:"locator"`
	Digest     string     `json:"digest"`
	ByteLength int64      `json:"byte_length"`
	MediaType  string     `json:"media_type"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (ref BlobRef) Validate(now time.Time) error {
	if strings.TrimSpace(ref.Store) == "" || strings.TrimSpace(ref.Locator) == "" || strings.TrimSpace(ref.MediaType) == "" {
		return fmt.Errorf("blob reference requires store, locator, and media_type")
	}
	if ref.ByteLength < 0 {
		return fmt.Errorf("blob reference byte_length must not be negative")
	}
	if len(ref.Digest) != 64 {
		return fmt.Errorf("blob reference digest must be a 64-character SHA-256 hex value")
	}
	if _, err := hex.DecodeString(ref.Digest); err != nil {
		return fmt.Errorf("blob reference digest is not hexadecimal: %w", err)
	}
	if ref.ExpiresAt != nil && !now.IsZero() && !now.Before(*ref.ExpiresAt) {
		return fmt.Errorf("blob reference is expired")
	}
	return nil
}

func (request GenerateRequest) Validate(maxInlineBytes int) (llm.Request, error) {
	if request.APIVersion != APIVersion {
		return llm.Request{}, fmt.Errorf("unsupported Activity payload version %q", request.APIVersion)
	}
	normalized, err := llm.NormalizeRequest(request.Request)
	if err != nil {
		return llm.Request{}, fmt.Errorf("request normalization failed: %w", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return llm.Request{}, fmt.Errorf("request encoding failed: %w", err)
	}
	if maxInlineBytes <= 0 {
		maxInlineBytes = DefaultInlineBytes
	}
	if len(encoded) > maxInlineBytes {
		return llm.Request{}, fmt.Errorf("inline request payload is %d bytes; limit is %d", len(encoded), maxInlineBytes)
	}
	if err := validateEmbeddedBlobRefs(encoded, time.Now()); err != nil {
		return llm.Request{}, err
	}
	return normalized, nil
}

func (request GenerateRequest) MarshalJSON() ([]byte, error) {
	if request.APIVersion == "" {
		request.APIVersion = APIVersion
	}
	if _, err := request.Validate(DefaultInlineBytes); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		APIVersion string      `json:"api_version"`
		Request    llm.Request `json:"request"`
	}{APIVersion: request.APIVersion, Request: request.Request})
}

func (request *GenerateRequest) UnmarshalJSON(data []byte) error {
	fields, err := decodeObject(data)
	if err != nil {
		return err
	}
	if err := checkUnknownFields(fields, "api_version", "request"); err != nil {
		return err
	}
	version, ok := fields["api_version"]
	if !ok {
		return fmt.Errorf("missing required JSON field %q", "api_version")
	}
	if err := json.Unmarshal(version, &request.APIVersion); err != nil {
		return fmt.Errorf("api_version: %w", err)
	}
	requestRaw, ok := fields["request"]
	if !ok {
		return fmt.Errorf("missing required JSON field %q", "request")
	}
	if err := json.Unmarshal(requestRaw, &request.Request); err != nil {
		return fmt.Errorf("request: %w", err)
	}
	_, err = request.Validate(DefaultInlineBytes)
	return err
}

func (response GenerateResponse) Validate(maxInlineBytes int) error {
	if response.APIVersion != APIVersion {
		return fmt.Errorf("unsupported Activity response version %q", response.APIVersion)
	}
	encoded, err := json.Marshal(response.Response)
	if err != nil {
		return fmt.Errorf("response encoding failed: %w", err)
	}
	if maxInlineBytes <= 0 {
		maxInlineBytes = DefaultInlineBytes
	}
	if len(encoded) > maxInlineBytes {
		return fmt.Errorf("inline response payload is %d bytes; limit is %d", len(encoded), maxInlineBytes)
	}
	return nil
}

func (response GenerateResponse) MarshalJSON() ([]byte, error) {
	if response.APIVersion == "" {
		response.APIVersion = APIVersion
	}
	if err := response.Validate(DefaultInlineBytes); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		APIVersion string         `json:"api_version"`
		Response   llm.Response   `json:"response"`
		Metadata   ResultMetadata `json:"metadata"`
	}{APIVersion: response.APIVersion, Response: response.Response, Metadata: response.Metadata})
}

func (response *GenerateResponse) UnmarshalJSON(data []byte) error {
	fields, err := decodeObject(data)
	if err != nil {
		return err
	}
	if err := checkUnknownFields(fields, "api_version", "response", "metadata"); err != nil {
		return err
	}
	if raw, ok := fields["api_version"]; !ok {
		return fmt.Errorf("missing required JSON field %q", "api_version")
	} else if err := json.Unmarshal(raw, &response.APIVersion); err != nil {
		return fmt.Errorf("api_version: %w", err)
	}
	if raw, ok := fields["response"]; !ok {
		return fmt.Errorf("missing required JSON field %q", "response")
	} else if err := json.Unmarshal(raw, &response.Response); err != nil {
		return fmt.Errorf("response: %w", err)
	}
	if raw, ok := fields["metadata"]; ok {
		if err := json.Unmarshal(raw, &response.Metadata); err != nil {
			return fmt.Errorf("metadata: %w", err)
		}
	}
	return response.Validate(DefaultInlineBytes)
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected JSON object key")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate JSON object key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = append(json.RawMessage(nil), value...)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON payload has trailing data")
		}
		return nil, err
	}
	return fields, nil
}

func checkUnknownFields(fields map[string]json.RawMessage, allowed ...string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("unknown JSON field %q", key)
		}
	}
	return nil
}

func validateEmbeddedBlobRefs(data []byte, now time.Time) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return walkEmbeddedBlobRefs(value, now)
}

func walkEmbeddedBlobRefs(value any, now time.Time) error {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			if err := walkEmbeddedBlobRefs(child, now); err != nil {
				return err
			}
		}
	case map[string]any:
		if _, ok := value["digest"]; ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var ref struct {
				Digest     string `json:"digest"`
				ByteLength int64  `json:"byte_length"`
				MediaType  string `json:"media_type"`
				Locator    string `json:"locator"`
			}
			if err := json.Unmarshal(encoded, &ref); err != nil {
				return err
			}
			if err := (BlobRef{Store: "embedded", Locator: ref.Locator, Digest: ref.Digest, ByteLength: ref.ByteLength, MediaType: ref.MediaType}).Validate(now); err != nil {
				return err
			}
		}
		for _, child := range value {
			if err := walkEmbeddedBlobRefs(child, now); err != nil {
				return err
			}
		}
	}
	return nil
}
