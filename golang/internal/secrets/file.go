package secrets

import (
	"context"
	"fmt"
	"io"
	"os"
)

func ReadSecretFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || maxBytes <= 0 {
		return nil, fmt.Errorf("secret file path and positive size limit are required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file must be regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer file.Close()
	reader := io.LimitReader(file, maxBytes+1)
	value, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	if int64(len(value)) > maxBytes {
		return nil, fmt.Errorf("secret file exceeds the configured size limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}
