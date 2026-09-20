package model

// Response is the unified API response format
type Response struct {
	Code int         `json:"code"`
	Data interface{} `json:"data"`
	Msg  string      `json:"msg"`
}

// PublicError is a panic payload that is safe to return to API clients.
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

// Error returns an error response
func Error(msg string) Response {
	return Response{Code: 500, Data: nil, Msg: msg}
}
