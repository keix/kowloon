package kowloon

import "errors"

// ErrNotImplemented signals that a Service method is wired but no
// implementation is attached yet. The HTTP layer maps it to 501 so
// stubbed builds are visibly incomplete rather than silently broken.
var ErrNotImplemented = errors.New("kowloon: not implemented")

// ErrBadRequest is the typed error a Service implementation returns
// when the caller's input is malformed in a way the HTTP layer's
// generic validation cannot catch (unknown schema_version, malformed
// source URI, etc.). The HTTP layer unwraps it to 400 so lower layers
// do not have to import net/http.
type ErrBadRequest struct{ Err error }

func (e ErrBadRequest) Error() string { return e.Err.Error() }
func (e ErrBadRequest) Unwrap() error { return e.Err }
