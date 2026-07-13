package admission

import "errors"

var (
	ErrOperationNotFound = errors.New("operation not found")
	ErrOperationConflict = errors.New("operation conflict")
	ErrInvalidToken      = errors.New("invalid dispatch token")
	ErrInvalidTransition = errors.New("invalid operation transition")
	ErrStateUnavailable  = errors.New("admission state unavailable")
)
