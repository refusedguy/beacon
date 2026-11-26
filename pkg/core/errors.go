package core

import (
	"errors"
	"fmt"
)

// ErrorCode classifies an application error.
type ErrorCode string

const (
	// CodeInvalidInput means the caller supplied an invalid value.
	CodeInvalidInput ErrorCode = "invalid_input"
	// CodeNotFound means the requested entity does not exist.
	CodeNotFound ErrorCode = "not_found"
	// CodeConflict means the operation conflicts with current state.
	CodeConflict ErrorCode = "conflict"
	// CodeUnavailable means a dependency could not be reached.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeInvalidTransition means a run cannot apply the requested event.
	CodeInvalidTransition ErrorCode = "invalid_transition"
	// CodeExhausted means an attempt budget was used up.
	CodeExhausted ErrorCode = "exhausted"
	// CodeBaseRed means the base branch has failing checks.
	CodeBaseRed ErrorCode = "base_red"
)

// AppError is an error that carries a stable code.
type AppError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

// Error renders the code, message and cause.
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause.
func (e *AppError) Unwrap() error { return e.Cause }

// Errorf builds an AppError with the given code and formatted message.
func Errorf(code ErrorCode, format string, args ...any) error {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WorkspaceConflict reports a dirty worktree that cannot be removed safely.
// The message stays stable because dispatch and MCP callers expose it in JSON.
func WorkspaceConflict(path string) error {
	return Errorf(CodeConflict, "refusing to remove dirty worktree %s", path)
}

// JoinWith keeps primary as the main error and appends extra under label. A nil
// extra leaves primary unchanged. Both errors stay in the chain for errors.Is.
func JoinWith(primary error, label string, extra error) error {
	if extra == nil {
		return primary
	}
	return fmt.Errorf("%w; %s: %w", primary, label, extra)
}

// CodeOf returns the code of an AppError in the error chain.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return CodeUnavailable
}
