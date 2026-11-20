// Package httproute provides a small HTTP router and a typed error contract.
// It uses only the standard library so transports can share one error shape.
package httproute

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier. Clients branch on Code
// and never parse Message or a log line.
type Code string

const (
	CodeInvalidArgument  Code = "invalid_argument"
	CodeNotFound         Code = "not_found"
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeUnauthorized     Code = "unauthorized"
	CodeForbidden        Code = "forbidden"
	CodeConflict         Code = "conflict"
	CodeInternal         Code = "internal"
)

// Error is the JSON body returned for a failed request.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError builds a typed error with an explicit code.
func NewError(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// InvalidArgument builds a client error for bad input. It maps to HTTP 400.
func InvalidArgument(format string, args ...any) *Error {
	return NewError(CodeInvalidArgument, format, args...)
}

// Status maps a code to its HTTP status. An unknown code maps to 500, so a new
// code is never served as a success.
func (c Code) Status() int {
	switch c {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// CodeOf extracts the code from err, unwrapping wrapped errors. It returns
// CodeInternal when err has no typed code. A nil error yields an empty code.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// WriteError writes err as a JSON error response. A plain error without a
// typed code becomes CodeInternal with a generic message, so internal details
// never reach the client.
func WriteError(w http.ResponseWriter, err error) {
	body := &Error{Code: CodeInternal, Message: "internal error"}
	var typed *Error
	if errors.As(err, &typed) {
		body = typed
	}
	writeJSON(w, body.Code.Status(), body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
