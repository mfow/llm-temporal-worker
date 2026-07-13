package openaichat

import (
	"bytes"
	"testing"
)

func FuzzDecodeStream(f *testing.F) {
	f.Add([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		_, _ = DecodeStream(bytes.NewReader(wire))
	})
}
