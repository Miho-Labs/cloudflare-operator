package cloudflare

import (
	"errors"
	"net/http"

	"github.com/cloudflare/cloudflare-go/v7"
)

// APIError returns the SDK error if err wraps one.
func APIError(err error) (*cloudflare.Error, bool) {
	var apiErr *cloudflare.Error
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// StatusCode returns the HTTP status of an API error, or 0.
func StatusCode(err error) int {
	if apiErr, ok := APIError(err); ok {
		return apiErr.StatusCode
	}
	return 0
}

// HasCode reports whether the API error carries the given Cloudflare error code.
func HasCode(err error, code int64) bool {
	apiErr, ok := APIError(err)
	if !ok {
		return false
	}
	for _, e := range apiErr.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

// IsNotFound reports whether the object does not exist on Cloudflare.
func IsNotFound(err error) bool {
	return StatusCode(err) == http.StatusNotFound
}

// IsRateLimited reports whether Cloudflare asked us to slow down.
func IsRateLimited(err error) bool {
	return StatusCode(err) == http.StatusTooManyRequests
}

// IsAuthError reports whether the token is invalid or lacks permissions.
func IsAuthError(err error) bool {
	code := StatusCode(err)
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// IsConflict reports whether the request conflicts with an existing object (duplicate name...).
func IsConflict(err error) bool {
	return StatusCode(err) == http.StatusConflict
}

// Message extracts the first human-readable message from an API error, falling back to err.Error().
func Message(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := APIError(err); ok && len(apiErr.Errors) > 0 {
		return apiErr.Errors[0].Message
	}
	return err.Error()
}
