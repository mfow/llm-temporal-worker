package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type DiagnosticSeverity string

const (
	DiagnosticInfo    DiagnosticSeverity = "info"
	DiagnosticWarning DiagnosticSeverity = "warning"
	DiagnosticError   DiagnosticSeverity = "error"
)

type Diagnostic struct {
	Code     string
	Severity DiagnosticSeverity
	Path     string
	Message  string
	Details  map[string]string
}

func (diagnostic Diagnostic) MarshalJSON() ([]byte, error) {
	if diagnostic.Code == "" {
		return nil, fmt.Errorf("diagnostic code must not be empty")
	}
	severity := diagnostic.Severity
	if severity == "" {
		severity = DiagnosticInfo
	}
	if severity != DiagnosticInfo && severity != DiagnosticWarning && severity != DiagnosticError {
		return nil, fmt.Errorf("diagnostic severity %q is invalid", severity)
	}
	if diagnostic.Message == "" {
		return nil, fmt.Errorf("diagnostic message must not be empty")
	}
	fields := map[string]any{
		"code":     diagnostic.Code,
		"severity": severity,
		"message":  diagnostic.Message,
	}
	if diagnostic.Path != "" {
		fields["path"] = diagnostic.Path
	}
	if len(diagnostic.Details) > 0 {
		fields["details"] = diagnostic.Details
	}
	return marshalObject(fields)
}

func decodeDiagnostic(data []byte) (Diagnostic, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Diagnostic{}, err
	}
	if err := checkUnknownFields(fields, "code", "severity", "path", "message", "details"); err != nil {
		return Diagnostic{}, err
	}
	code, err := requiredString(fields, "code")
	if err != nil {
		return Diagnostic{}, err
	}
	severityValue, _, err := optionalString(fields, "severity")
	if err != nil {
		return Diagnostic{}, err
	}
	severity := DiagnosticSeverity(severityValue)
	if severity == "" {
		severity = DiagnosticInfo
	}
	if severity != DiagnosticInfo && severity != DiagnosticWarning && severity != DiagnosticError {
		return Diagnostic{}, fmt.Errorf("diagnostic severity %q is invalid", severity)
	}
	path, _, err := optionalString(fields, "path")
	if err != nil {
		return Diagnostic{}, err
	}
	message, err := requiredString(fields, "message")
	if err != nil {
		return Diagnostic{}, err
	}
	var details map[string]string
	if raw, ok := fields["details"]; ok {
		if err := decodeJSON(raw, &details); err != nil {
			return Diagnostic{}, fmt.Errorf("diagnostic details: %w", err)
		}
	}
	return Diagnostic{Code: code, Severity: severity, Path: path, Message: message, Details: details}, nil
}

func decodeDiagnostics(data []byte) ([]Diagnostic, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("diagnostics must be an array")
	}
	var values []json.RawMessage
	if err := decodeJSON(data, &values); err != nil {
		return nil, err
	}
	result := make([]Diagnostic, 0, len(values))
	for index, value := range values {
		diagnostic, err := decodeDiagnostic(value)
		if err != nil {
			return nil, fmt.Errorf("diagnostic %d: %w", index, err)
		}
		result = append(result, diagnostic)
	}
	return result, nil
}
