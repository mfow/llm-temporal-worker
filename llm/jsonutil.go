package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var errJSONTrailingData = errors.New("json value has trailing data")

// decodeObject decodes one JSON object while rejecting duplicate keys. The
// nested values are retained as raw JSON so tagged unions can validate their
// own closed fields.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("expected JSON object")
	}

	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("expected JSON object key")
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

	token, err = decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return nil, errors.New("expected end of JSON object")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return fields, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errJSONTrailingData
}

func decodeJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
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

func requireField(fields map[string]json.RawMessage, key string) (json.RawMessage, error) {
	value, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("missing required JSON field %q", key)
	}
	return value, nil
}

func optionalString(fields map[string]json.RawMessage, key string) (string, bool, error) {
	value, ok := fields[key]
	if !ok {
		return "", false, nil
	}
	var result string
	if err := decodeJSON(value, &result); err != nil {
		return "", true, fmt.Errorf("%s: %w", key, err)
	}
	return result, true, nil
}

func requiredString(fields map[string]json.RawMessage, key string) (string, error) {
	value, err := requireField(fields, key)
	if err != nil {
		return "", err
	}
	result, ok, err := optionalString(map[string]json.RawMessage{key: value}, key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("missing required JSON field %q", key)
	}
	return result, nil
}

func optionalBool(fields map[string]json.RawMessage, key string) (bool, bool, error) {
	value, ok := fields[key]
	if !ok {
		return false, false, nil
	}
	var result bool
	if err := decodeJSON(value, &result); err != nil {
		return false, true, fmt.Errorf("%s: %w", key, err)
	}
	return result, true, nil
}

func optionalInt(fields map[string]json.RawMessage, key string) (int, bool, error) {
	value, ok := fields[key]
	if !ok {
		return 0, false, nil
	}
	var result int
	if err := decodeJSON(value, &result); err != nil {
		return 0, true, fmt.Errorf("%s: %w", key, err)
	}
	return result, true, nil
}

func copyRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func copyBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func validRawJSON(value json.RawMessage) bool {
	return len(value) > 0 && json.Valid(value)
}

func marshalObject(fields map[string]any) ([]byte, error) {
	return json.Marshal(fields)
}
