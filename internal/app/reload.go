package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxReloadFileBytes = 4 << 20

// PublishedReloadError reports cleanup that did not finish before a reload
// returned, after the replacement snapshot was atomically published. Callers
// must not report this as a rejected configuration: the active snapshot has
// already changed and the underlying cleanup outcome is separate from the
// reload outcome.
type PublishedReloadError struct {
	cause error
}

func (err *PublishedReloadError) Error() string {
	return fmt.Sprintf("replacement published but previous snapshot cleanup did not finish: %v", err.cause)
}

func (err *PublishedReloadError) Unwrap() error { return err.cause }

// IsPublishedReloadError reports whether err occurred after a replacement
// snapshot was successfully published.
func IsPublishedReloadError(err error) bool {
	var published *PublishedReloadError
	return errors.As(err, &published)
}

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
