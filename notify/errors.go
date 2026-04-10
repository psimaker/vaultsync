package main

import "fmt"

// HTTPStatusError indicates a non-200 response from an HTTP dependency.
type HTTPStatusError struct {
	Component  string
	URL        string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "http status error"
	}
	if e.Body == "" {
		return fmt.Sprintf("%s returned HTTP %d for %s", e.Component, e.StatusCode, e.URL)
	}
	return fmt.Sprintf("%s returned HTTP %d for %s: %s", e.Component, e.StatusCode, e.URL, e.Body)
}
