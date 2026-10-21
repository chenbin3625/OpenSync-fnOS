package model

// Response is the unified API response format
type Response struct {
	Code int         `json:"code"`
	Data interface{} `json:"data"`
	Msg  string      `json:"msg"`
}

// PublicError carries a localized message that is safe to return to API
// clients. It is propagated as an ordinary error value: handlers detect it with
// errors.As (which still matches when a service wraps it with fmt.Errorf("%w"))
// and return its text instead of the generic internal error.
//
// It is NOT a panic payload: the server's recover middleware logs any panic and
// answers with model.Error(msg.InternalError), so panicking with PublicError
// would lose the message. Return it, do not panic with it.
type PublicError string

func (e PublicError) Error() string {
	return string(e)
}

// Success returns a success response
func Success(data interface{}) Response {
	if data == nil {
		return Response{Code: 200, Data: nil, Msg: "success"}
	}
	return Response{Code: 200, Data: data, Msg: "success"}
}

// Error returns a failure response with the fixed business code 500.
//
// This helper does not decide the HTTP status; callers choose it (the unified
// envelope pattern means some handlers answer HTTP 200 with Code 500 while
// others use 4xx/5xx, see handler.respondError). Validation/conflict failures
// that should be reported as client errors carry their own user-facing text but
// still get Code 500 here; a code-aware variant would require handler changes.
func Error(msg string) Response {
	return Response{Code: 500, Data: nil, Msg: msg}
}

// NotFoundError is a "row does not exist" failure. Error() is the localized,
// user-facing text (unchanged from the plain errors the mappers used to
// return, so existing message comparisons keep working), while Is matches the
// stable sentinel so callers can use errors.Is instead of comparing strings.
// It is safe to show to API clients even when wrapped with fmt.Errorf("%w").
type NotFoundError struct {
	Sentinel error
	Message  string
}

func (e *NotFoundError) Error() string { return e.Message }

// Is reports whether target is the sentinel this error stands for.
func (e *NotFoundError) Is(target error) bool { return target == e.Sentinel }

// PublicMessage returns the text that may be returned to API clients.
func (e *NotFoundError) PublicMessage() string { return e.Message }

// PublicMessager is implemented by errors whose message is safe to return to
// API clients even when they arrive wrapped.
type PublicMessager interface {
	PublicMessage() string
}
