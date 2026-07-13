package bedrockmessages

import (
	"bytes"
	"testing"
)

func FuzzDecodeStream(f *testing.F) {
	f.Add([]byte("event: message_stop\ndata: {}\n\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		_, _ = DecodeStream(bytes.NewReader(wire))
	})
}
