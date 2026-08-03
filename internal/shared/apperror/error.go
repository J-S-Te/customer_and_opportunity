package apperror

import (
	"errors"
	"net/http"
)

// Error 是稳定的 API 错误契约；Cause 仅供服务端诊断，有意排除在 JSON 响应之外。
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	Details    any
	Cause      error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

func New(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}

func WithDetails(err *Error, details any) *Error {
	clone := *err
	clone.Details = details
	return &clone
}

func Wrap(err error, status int, code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status, Cause: err}
}

func As(err error) *Error {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Wrap(err, http.StatusInternalServerError, "COMMON_INTERNAL_ERROR", "internal server error")
}

var (
	ErrUnauthenticated = New(http.StatusUnauthorized, "COMMON_UNAUTHENTICATED", "authentication required")
	ErrForbidden       = New(http.StatusForbidden, "COMMON_FORBIDDEN", "permission denied")
	ErrNotFound        = New(http.StatusNotFound, "COMMON_NOT_FOUND", "resource not found")
	ErrVersionConflict = New(http.StatusConflict, "COMMON_VERSION_CONFLICT", "resource was modified by another request")
)
