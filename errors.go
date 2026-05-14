package engram

import "fmt"

// Error is returned for any non-2xx response from the Engram API. The HTTP
// status code lives on Status; the parsed JSON body (or raw text fallback)
// lives on Body.
//
// Use errors.As to inspect:
//
//	var apiErr *engram.Error
//	if errors.As(err, &apiErr) && apiErr.Status == 412 {
//	    // BYOK not configured
//	}
type Error struct {
	Message string
	Status  int
	Body    any
}

func (e *Error) Error() string {
	return e.Message
}

func newError(status int, body any) *Error {
	detail := body
	if m, ok := body.(map[string]any); ok {
		if d, has := m["error"]; has {
			detail = d
		}
	}
	var msg string
	switch v := detail.(type) {
	case nil:
		msg = fmt.Sprintf("Engram API %d", status)
	case string:
		msg = fmt.Sprintf("Engram API %d: %s", status, v)
	default:
		msg = fmt.Sprintf("Engram API %d: %v", status, v)
	}
	return &Error{Message: msg, Status: status, Body: body}
}
