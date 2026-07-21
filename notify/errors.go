package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type configurationError struct {
	kind   string
	action string
	fields []string
	cause  error
}

func newConfigurationError(kind, action string, cause error, fields ...string) error {
	return &configurationError{
		kind:   kind,
		action: action,
		fields: append([]string(nil), fields...),
		cause:  cause,
	}
}

func (e *configurationError) Error() string {
	if e == nil || e.cause == nil {
		return "invalid runtime configuration"
	}
	return e.cause.Error()
}

func (e *configurationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

var allowedOperationalConfigFields = map[string]bool{
	"DEBOUNCE_SECONDS":        true,
	"RELAY_URL":               true,
	"STALE_RETRIGGER_SECONDS": true,
	"STARTUP_ANNOUNCE":        true,
	"SYNCTHING_API_KEY":       true,
	"SYNCTHING_API_URL":       true,
	"SYNCTHING_CONFIG":        true,
}

func configurationErrorFields(err error) string {
	var configErr *configurationError
	if !errors.As(err, &configErr) {
		return ""
	}

	safe := make([]string, 0, len(configErr.fields))
	for _, field := range configErr.fields {
		if allowedOperationalConfigFields[field] {
			safe = append(safe, field)
		}
	}
	return strings.Join(safe, ",")
}

func configurationErrorAction(err error) string {
	var configErr *configurationError
	if !errors.As(err, &configErr) || configErr.action == "" {
		return "exit"
	}
	return configErr.action
}

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
	var configErr *configurationError
	if errors.As(err, &configErr) {
		return "configuration_" + configErr.kind
	}

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
