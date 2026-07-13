package openairesponses

import (
	"bytes"
	"testing"
)

func FuzzDecodeStream(f *testing.F) {
	f.Add([]byte("event: response.completed\ndata: {}\n\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		_, _ = DecodeStream(bytes.NewReader(wire))
	})
}
