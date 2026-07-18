package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// SemanticRoundTrip converts a semantic fixture through one adapter's native
// representation and returns the lifted semantic value. Enforced adapter
// fixture tests use it to prove reversible behavior without importing another
// provider's SDK types.
type SemanticRoundTrip func([]byte) ([]byte, error)

// StreamAssembler decodes an adapter's captured stream fixture into the
// semantic response it assembles. Enforced adapter fixture tests use it to
// compare the assembled result with their non-stream response fixture.
type StreamAssembler func([]byte) ([]byte, error)

// VerifySemanticRoundTrip verifies semantic -> adapter -> semantic equivalence.
// Generated fields are removed from both JSON values before comparison; callers
// should pass only the profile's checked-in generated_field_exemptions.
func VerifySemanticRoundTrip(semantic []byte, roundTrip SemanticRoundTrip, generatedFields []string) error {
	if roundTrip == nil {
		return fmt.Errorf("semantic round-trip function is required")
	}
	roundTripped, err := roundTrip(append([]byte(nil), semantic...))
	if err != nil {
		return fmt.Errorf("semantic round-trip conversion failed")
	}
	if err := verifyJSONEquivalent(semantic, roundTripped, generatedFields); err != nil {
		return fmt.Errorf("semantic round-trip differs after generated fields are ignored")
	}
	return nil
}

// VerifyStreamAssemblyEquivalent verifies that a captured stream assembles to
// the same semantic response as the non-stream fixture. Generated fields use
// the same narrowly-scoped exemptions as semantic round-trip checks.
func VerifyStreamAssemblyEquivalent(events, nonStreaming []byte, assemble StreamAssembler, generatedFields []string) error {
	if assemble == nil {
		return fmt.Errorf("stream assembler is required")
	}
	assembled, err := assemble(append([]byte(nil), events...))
	if err != nil {
		return fmt.Errorf("stream assembly failed")
	}
	if err := verifyJSONEquivalent(nonStreaming, assembled, generatedFields); err != nil {
		return fmt.Errorf("stream assembly differs from the non-stream response")
	}
	return nil
}

func verifyJSONEquivalent(left, right []byte, generatedFields []string) error {
	leftValue, err := decodeJSON(left)
	if err != nil {
		return fmt.Errorf("left semantic fixture is invalid JSON")
	}
	rightValue, err := decodeJSON(right)
	if err != nil {
		return fmt.Errorf("right semantic fixture is invalid JSON")
	}
	for _, field := range generatedFields {
		parts, ok := generatedFieldPath(field)
		if !ok {
			return fmt.Errorf("generated field exemption path is invalid")
		}
		deleteJSONPath(leftValue, parts)
		deleteJSONPath(rightValue, parts)
	}
	if !reflect.DeepEqual(leftValue, rightValue) {
		return fmt.Errorf("semantic JSON differs")
	}
	return nil
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func generatedFieldPath(field string) ([]string, bool) {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, false
	}
	parts := strings.Split(field, ".")
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
	}
	return parts, true
}

func deleteJSONPath(value any, path []string) {
	if len(path) == 0 {
		return
	}
	switch value := value.(type) {
	case map[string]any:
		if path[0] == "*" {
			for key, child := range value {
				if len(path) == 1 {
					delete(value, key)
					continue
				}
				deleteJSONPath(child, path[1:])
			}
			return
		}
		child, ok := value[path[0]]
		if !ok {
			return
		}
		if len(path) == 1 {
			delete(value, path[0])
			return
		}
		deleteJSONPath(child, path[1:])
	case []any:
		if path[0] == "*" {
			for _, child := range value {
				deleteJSONPath(child, path[1:])
			}
			return
		}
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(value) {
			return
		}
		deleteJSONPath(value[index], path[1:])
	}
}
