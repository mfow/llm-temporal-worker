package clientconfig

import "testing"

func TestBaseURLRejectsUnsafeForms(t *testing.T) {
	for _, value := range []string{"", "api.example.com", "https://user@example.com", "https://example.com/path?q=1", "http://example.com"} {
		if _, err := BaseURL(value); err == nil {
			t.Errorf("BaseURL(%q) unexpectedly succeeded", value)
		}
	}
	for _, value := range []string{"https://api.example.com/v1", "http://127.0.0.1:8080"} {
		if got, err := BaseURL(value); err != nil || got[len(got)-1] != '/' {
			t.Errorf("BaseURL(%q) = %q, %v", value, got, err)
		}
	}
}
