package service

import (
	"errors"
	"opensync/internal/model"
)

func publicError(msg string) error {
	return model.PublicError(msg)
}

// publicErrorIf turns err into a model.PublicError when it carries a message
// that is safe to show: an error marked public by its type (such as the
// mapper's *model.NotFoundError, found with errors.As so wrapping does not
// hide it), or one whose text equals one of publicMessages. Anything else is
// returned unchanged and stays internal. Callers that need to branch on the
// failure should test errors.Is(err, mapper.ErrXxxNotFound) before calling.
func publicErrorIf(err error, publicMessages ...string) error {
	if err == nil {
		return nil
	}
	var public model.PublicMessager
	if errors.As(err, &public) {
		return model.PublicError(public.PublicMessage())
	}
	for _, m := range publicMessages {
		if err.Error() == m {
			return model.PublicError(m)
		}
	}
	return err
}
