package streamdecode

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePreservesFoldedDataAndIgnoresTransportMetadata(t *testing.T) {
	input := strings.Join([]string{
		": keep-alive",
		"id: 42",
		"retry: 1000",
		"event: message",
		"data: first",
		"data:second",
		"",
		"event: empty",
		"",
		"",
	}, "\r\n")

	got, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []SSE{
		{Event: "message", Data: []byte("first\nsecond")},
		{Event: "empty"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed events = %#v, want %#v", got, want)
	}
}

func TestParseRejectsInvalidStreamBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unsupported field", input: "unknown: value\n\n", want: "unsupported SSE field"},
		{name: "unterminated event", input: "data: payload\n", want: "unterminated event"},
		{name: "oversized line", input: strings.Repeat("x", 8<<20+1) + "\n", want: "read SSE stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil reader error = %v, want nil-reader failure", err)
	}
}
