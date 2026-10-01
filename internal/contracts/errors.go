package contracts

import "errors"

// ErrorKind is the stable classification exposed by the transport layer.
type ErrorKind string

const (
	KindInvalid  ErrorKind = "invalid"
	KindNotFound ErrorKind = "not_found"
	KindConflict ErrorKind = "conflict"
	KindInternal ErrorKind = "internal"
)

// ErrorDetail identifies a single failing element inside a batch request.
type ErrorDetail struct {
	// Index is the 1-based position of the offending entry inside the batch.
	// It is zero when the failure targets the request envelope itself.
	Index int `json:"index,omitempty"`
	// Field is the optional name of the offending request field.
	Field  string `json:"field,omitempty"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Error carries a machine-readable code without coupling domain code to HTTP.
type Error struct {
	Kind    ErrorKind
	Code    string
	Message string
	Cause   error
	Details []ErrorDetail
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func Invalid(code, message string) error {
	return &Error{Kind: KindInvalid, Code: code, Message: message}
}

func NotFound(code, message string) error {
	return &Error{Kind: KindNotFound, Code: code, Message: message}
}

func Conflict(code, message string) error {
	return &Error{Kind: KindConflict, Code: code, Message: message}
}

func Internal(code, message string, cause error) error {
	return &Error{Kind: KindInternal, Code: code, Message: message, Cause: cause}
}

// InvalidBatch attaches per-entry details to an invalid batch envelope error.
func InvalidBatch(code, message string, details []ErrorDetail) error {
	return &Error{Kind: KindInvalid, Code: code, Message: message, Details: details}
}

// ConflictBatch attaches per-entry details to a conflicting batch error.
func ConflictBatch(code, message string, details []ErrorDetail) error {
	return &Error{Kind: KindConflict, Code: code, Message: message, Details: details}
}

func AsError(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
