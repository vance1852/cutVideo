// Package apierr maps domain and infrastructure failures onto the stable public
// error contract used by the HTTP layer.
package apierr

import (
	"context"
	"errors"
	"net/http"
)

// Code is a stable machine readable error identifier returned to clients.
type Code string

// Public error codes. These values are part of the API contract and must not
// change once released.
const (
	CodeInvalidRequest   Code = "invalid_request"
	CodeUnauthenticated  Code = "unauthenticated"
	CodeForbidden        Code = "forbidden"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodePreconditionFail Code = "precondition_failed"
	CodeQuotaExhausted   Code = "quota_exhausted"
	CodeTimeout          Code = "request_timeout"
	CodeCanceled         Code = "request_canceled"
	CodeInternal         Code = "internal_error"
)

// Error is a public error carrying a stable code plus an operator readable
// message. Underlying causes are preserved for logs but never serialized.
type Error struct {
	Code    Code
	Message string
	Status  int
	Details map[string]string
	cause   error
}

// New builds a public error without an underlying cause.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message, Status: statusFor(code)}
}

// Wrap builds a public error that keeps the underlying cause for logging.
func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Status: statusFor(code), cause: cause}
}

// WithDetail attaches a structured detail entry and returns the same error.
func (e *Error) WithDetail(key, value string) *Error {
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	e.Details[key] = value
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.cause.Error()
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus returns the response status for the error.
func (e *Error) HTTPStatus() int {
	if e.Status == 0 {
		return statusFor(e.Code)
	}
	return e.Status
}

// From converts any error into a public error, preserving an existing public
// error if one is present in the chain.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(CodeCanceled, "request was canceled before completion", err)
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(CodeTimeout, "request exceeded its deadline", err)
	default:
		return Wrap(CodeInternal, "unexpected internal failure", err)
	}
}

// Interrupted reports whether the failure came from the call being cut short
// rather than from the peer answering with a failure.
func Interrupted(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsCode reports whether the error chain carries the given public code.
func IsCode(err error, code Code) bool {
	var public *Error
	if !errors.As(err, &public) {
		return false
	}
	return public.Code == code
}

func statusFor(code Code) int {
	switch code {
	case CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodePreconditionFail:
		return http.StatusPreconditionFailed
	case CodeQuotaExhausted:
		return http.StatusTooManyRequests
	case CodeTimeout:
		return http.StatusGatewayTimeout
	case CodeCanceled:
		return 499
	default:
		return http.StatusInternalServerError
	}
}
