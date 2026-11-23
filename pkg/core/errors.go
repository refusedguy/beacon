package core

import "fmt"

type ErrorCode string

const (
	CodeInvalidInput      ErrorCode = "invalid_input"
	CodeNotFound          ErrorCode = "not_found"
	CodeConflict          ErrorCode = "conflict"
	CodeUnavailable       ErrorCode = "unavailable"
	CodeInvalidTransition ErrorCode = "invalid_transition"
	CodeExhausted         ErrorCode = "exhausted"
	CodeBaseRed           ErrorCode = "base_red"
)

type AppError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Cause }

func Errorf(code ErrorCode, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	if e, ok := err.(*AppError); ok {
		return e.Code
	}
	return CodeUnavailable
}
