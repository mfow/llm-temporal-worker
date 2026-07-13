package llm

import "encoding/json"

// NormalizeRequest fills deterministic v1 defaults and returns an independent
// value. JSON round-tripping through the public contract also copies byte
// slices, maps, and typed union values so callers cannot mutate the normalized
// request through the original input.
func NormalizeRequest(request Request) (Request, error) {
	if request.APIVersion == "" {
		request.APIVersion = APIVersion
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Request{}, err
	}
	var normalized Request
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return Request{}, err
	}
	return normalized, nil
}
