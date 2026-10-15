package kafkaconsumer

import "errors"

// permanentError marks a handler failure that retrying cannot fix (bad
// payload, unknown schema, ...). The runner sends it straight to the DLQ.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the runner skips retries and dead-letters the
// message immediately. Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or anything it wraps, came from Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}
