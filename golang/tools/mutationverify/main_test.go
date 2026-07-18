package main

import (
	"strings"
	"testing"
)

func TestReplaceExactlyOnce(t *testing.T) {
	replaced, err := replaceExactlyOnce([]byte("before target after"), []byte("target"), []byte("replacement"))
	if err != nil || string(replaced) != "before replacement after" {
		t.Fatalf("replaceExactlyOnce() = %q, %v", replaced, err)
	}
	for _, source := range [][]byte{[]byte("missing"), []byte("target target")} {
		if _, err := replaceExactlyOnce(source, []byte("target"), []byte("replacement")); err == nil {
			t.Fatalf("replaceExactlyOnce(%q) accepted a non-unique source", source)
		}
	}
}

func TestTrimOutputBoundsDiagnostic(t *testing.T) {
	if got := trimOutput([]byte("  compact output\n")); got != "compact output" {
		t.Fatalf("trimOutput compact = %q", got)
	}
	long := strings.Repeat("x", 3<<10)
	if got := trimOutput([]byte(long)); len(got) != (2<<10)+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("trimOutput long length/suffix = %d/%q", len(got), got[len(got)-3:])
	}
}
