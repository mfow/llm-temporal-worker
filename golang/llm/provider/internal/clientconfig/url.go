package clientconfig

import (
	"fmt"
	"net/url"
	"strings"
)

// BaseURL validates an adapter endpoint without accepting credentials or
// browser-only URL features. Plain HTTP is reserved for loopback test
// servers; deployed endpoints must use HTTPS.
func BaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base URL must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("base URL must not contain userinfo, query, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", fmt.Errorf("base URL must use HTTPS outside loopback")
	}
	return strings.TrimRight(raw, "/") + "/", nil
}

func Secret(name, value string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
