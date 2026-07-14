package app

import (
	"context"
	"fmt"
	"io"
	"os"
)

const maxReloadFileBytes = 4 << 20

// ReloadFile reads a complete replacement before compiling it. A read or
// validation error leaves the currently published snapshot untouched.
func (app *App) ReloadFile(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("configuration path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read configuration file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReloadFileBytes+1))
	if err != nil {
		return fmt.Errorf("read configuration file: %w", err)
	}
	if len(data) > maxReloadFileBytes {
		return fmt.Errorf("configuration file exceeds safe size")
	}
	return app.Reload(ctx, data)
}
