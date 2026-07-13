package streamdecode

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// SSE is one server-sent event after transport fragmentation and line folding
// have been removed. Data lines retain their protocol-mandated newline joins.
type SSE struct {
	Event string
	Data  []byte
}

// Parse reads an SSE stream without imposing a network read size. It rejects
// oversized individual lines and a truncated event so callers cannot silently
// accept an incomplete terminal record.
func Parse(reader io.Reader) ([]SSE, error) {
	if reader == nil {
		return nil, fmt.Errorf("stream reader is nil")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 8<<20)
	var eventName string
	var data bytes.Buffer
	result := make([]SSE, 0)
	dispatch := func() {
		if eventName == "" && data.Len() == 0 {
			return
		}
		result = append(result, SSE{Event: eventName, Data: append([]byte(nil), data.Bytes()...)})
		eventName = ""
		data.Reset()
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			dispatch()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data.WriteString(value)
			continue
		}
		// id/retry fields are transport metadata and are intentionally not
		// promoted into semantic provider events.
		if strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		return nil, fmt.Errorf("unsupported SSE field %q", line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}
	if eventName != "" || data.Len() > 0 {
		return nil, fmt.Errorf("SSE stream ended with an unterminated event")
	}
	return result, nil
}
