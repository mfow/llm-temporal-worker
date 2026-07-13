package contract

import "testing"

func TestRetryServerStartsEmpty(t *testing.T) {
	server := NewRetryServer(t)
	if server.URL == "" || server.Count() != 0 {
		t.Fatalf("retry server = %#v", server)
	}
}
