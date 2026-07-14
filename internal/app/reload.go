package app

import (
	"context"
	"fmt"
	"os"
)

// ReloadFile reads a complete replacement before compiling it. A read or
// validation error leaves the currently published snapshot untouched.
func (app *App) ReloadFile(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("configuration path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read configuration file: %w", err)
	}
	return app.Reload(ctx, data)
}
