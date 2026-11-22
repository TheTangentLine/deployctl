package steps

import "errors"

// How clients report errors
//
// The worker decides whether to retry a step from the error it returns:
//
//   - Temporary problems (network errors, timeouts, HTTP 5xx, HTTP 429):
//     wrap with Retryable. The worker retries the step with backoff.
//   - Something that does not exist (file, key, Argo CD app, namespace):
//     wrap ErrNotFound with fmt.Errorf("...: %w", ErrNotFound). Permanent.
//   - Anything else: return as is. Permanent; the request fails.

// ErrNotFound means the file, key, application or namespace does not exist.
var ErrNotFound = errors.New("not found")

type retryableError struct{ err error }

func (r retryableError) Error() string { return r.err.Error() }
func (r retryableError) Unwrap() error { return r.err }

// Retryable marks err as temporary, so the worker will try the step again.
// Retryable(nil) returns nil.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return retryableError{err}
}

// IsRetryable reports whether err, or any error it wraps, was marked Retryable.
func IsRetryable(err error) bool {
	var r retryableError
	return errors.As(err, &r)
}
