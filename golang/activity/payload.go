package activity

import (
	"encoding/json"
	"fmt"
	"time"
)

// PayloadLimits are application-level limits below Temporal's service limits.
// They are enforced before the request reaches an Activity implementation.
type PayloadLimits struct {
	MaxInlineBytes int
}

func (limits PayloadLimits) inlineBytes() int {
	if limits.MaxInlineBytes <= 0 {
		return DefaultInlineBytes
	}
	return limits.MaxInlineBytes
}

func MarshalRequest(request GenerateRequest, limits PayloadLimits) ([]byte, error) {
	if _, err := request.Validate(limits.inlineBytes()); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func UnmarshalRequest(data []byte, limits PayloadLimits) (GenerateRequest, error) {
	var request GenerateRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return GenerateRequest{}, err
	}
	if _, err := request.Validate(limits.inlineBytes()); err != nil {
		return GenerateRequest{}, err
	}
	return request, nil
}

func MarshalResponse(response GenerateResponse, limits PayloadLimits) ([]byte, error) {
	if err := response.Validate(limits.inlineBytes()); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func UnmarshalResponse(data []byte, limits PayloadLimits) (GenerateResponse, error) {
	var response GenerateResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return GenerateResponse{}, err
	}
	if err := response.Validate(limits.inlineBytes()); err != nil {
		return GenerateResponse{}, err
	}
	return response, nil
}

func ValidateBlobRef(ref BlobRef, nowUnixNano int64) error {
	if err := ref.Validate(time.Unix(0, nowUnixNano)); err != nil {
		return fmt.Errorf("invalid blob reference: %w", err)
	}
	return nil
}
