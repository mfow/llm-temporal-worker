package schema

import (
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidationError is a bounded, safe schema-instance error. Instance holds no
// value by design; only the canonical pointer, keyword, and safe explanation
// are exposed.
type ValidationError struct {
	Path     string
	Keyword  string
	Message  string
	Instance string
}

func (err *ValidationError) Error() string {
	path := err.Path
	if path == "" {
		path = "/"
	}
	if err.Keyword == "" {
		return fmt.Sprintf("schema validation failed at %s: %s", path, err.Message)
	}
	return fmt.Sprintf("schema validation failed at %s (%s): %s", path, err.Keyword, err.Message)
}

func normalizeValidationError(err error) error {
	validationErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return fmt.Errorf("schema validation failed")
	}
	output := validationErr.BasicOutput()
	leaf := firstOutputError(output)
	if leaf == nil {
		return &ValidationError{Message: "instance does not satisfy schema"}
	}
	keyword := lastPointerToken(leaf.KeywordLocation)
	message := "instance does not satisfy schema"
	if leaf.Error != nil {
		message = leaf.Error.String()
	}
	return &ValidationError{
		Path:     leaf.InstanceLocation,
		Keyword:  keyword,
		Message:  message,
		Instance: "",
	}
}

func firstOutputError(output *jsonschema.OutputUnit) *jsonschema.OutputUnit {
	if output == nil {
		return nil
	}
	if output.Error != nil {
		return output
	}
	for index := range output.Errors {
		if leaf := firstOutputError(&output.Errors[index]); leaf != nil {
			return leaf
		}
	}
	return nil
}

func lastPointerToken(pointer string) string {
	if pointer == "" || pointer == "#" {
		return ""
	}
	if index := strings.LastIndexByte(pointer, '/'); index >= 0 {
		pointer = pointer[index+1:]
	}
	return strings.ReplaceAll(strings.ReplaceAll(pointer, "~1", "/"), "~0", "~")
}
