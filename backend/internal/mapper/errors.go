package mapper

import (
	"errors"
	"opensync/internal/model"
	"opensync/internal/msg"
)

// Sentinel errors for missing rows. Mapper functions return a
// *model.NotFoundError wrapping one of these: errors.Is(err, ErrJobNotFound)
// identifies the case, and err.Error() is still the localized message the
// API has always returned.
var (
	ErrJobNotFound    = errors.New("job not found")
	ErrTaskNotFound   = errors.New("task not found")
	ErrNotifyNotFound = errors.New("notify not found")
	ErrAlistNotFound  = errors.New("alist not found")
)

func notFound(sentinel error, key string) error {
	return &model.NotFoundError{Sentinel: sentinel, Message: msg.T(key)}
}

// Sentinel errors for request rejections raised by the mapper itself. Callers
// can branch on them with errors.Is instead of comparing localized text, while
// the message (and therefore the API response) stays exactly what it was.
var (
	ErrListTooLarge      = errors.New("list too large")
	ErrInvalidPagination = errors.New("invalid pagination")
	ErrJobExists         = errors.New("job already exists")
)

// publicError is a user-facing failure with a stable machine-readable sentinel.
// It implements model.PublicMessager, so the handler/service layers keep
// returning its message even when a caller wraps it with fmt.Errorf("%w").
type publicError struct {
	sentinel error
	message  string
}

func (e *publicError) Error() string { return e.message }

func (e *publicError) Is(target error) bool { return target == e.sentinel }

func (e *publicError) PublicMessage() string { return e.message }

func listTooLargeError() error {
	return &publicError{sentinel: ErrListTooLarge, message: msg.T(msg.ListTooLarge)}
}

func invalidPaginationError() error {
	return &publicError{sentinel: ErrInvalidPagination, message: msg.T(msg.LostPart)}
}

// jobExistsError turns a UNIQUE(srcPath, dstPath, alistId) violation into a
// message the user can act on instead of a generic "internal error".
func jobExistsError() error {
	return &publicError{sentinel: ErrJobExists, message: msg.T(msg.JobExists)}
}
