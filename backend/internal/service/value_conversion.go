package service

import "opensync/internal/model"

func publicError(msg string) error {
	return model.PublicError(msg)
}

func publicErrorIf(err error, publicMessages ...string) error {
	if err == nil {
		return nil
	}
	for _, m := range publicMessages {
		if err.Error() == m {
			return model.PublicError(m)
		}
	}
	return err
}
