package client

import "fmt"

// HTTPError represents an HTTP error with status code
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsClientError returns true if the status code is a 4xx client error
func (e *HTTPError) IsClientError() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500
}

// IsServerError returns true if the status code is a 5xx server error
func (e *HTTPError) IsServerError() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
}

// NewHTTPError creates a new HTTPError
func NewHTTPError(statusCode int, message string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Message:    message,
	}
}

// ExtractStatusCode extracts HTTP status code from an error if it's an HTTPError
func ExtractStatusCode(err error) (int, bool) {
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode, true
	}
	return 0, false
}
