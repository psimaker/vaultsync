package main

import (
	"context"
	"errors"
	"fmt"
)

// HTTPStatusError indicates a non-200 response from an HTTP dependency.
type HTTPStatusError struct {
	Component  string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "http status error"
	}
	return fmt.Sprintf("%s returned HTTP %d", e.Component, e.StatusCode)
}

// operationalErrorKind converts arbitrary dependency errors into a bounded,
// non-sensitive category for logs. The original error stays in memory for
// control flow and user-invoked diagnostics, but identifiers, endpoint URLs,
// paths, response bodies, and library-specific error strings never enter the
// operational log stream.
func operationalErrorKind(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case isSubscriptionInactive(err):
		return "subscription_inactive"
	case isFatal(err):
		return "configuration"
	}

	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return "http_status"
	}

	var rateLimited *rateLimitError
	if errors.As(err, &rateLimited) {
		return "rate_limited"
	}

	var transient *transientError
	if errors.As(err, &transient) {
		return "transport"
	}

	return "operation_failed"
}
