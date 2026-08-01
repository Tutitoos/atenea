package contract

import (
	"errors"
	"fmt"
)

// FailureKind is one of the few common bins that every adapter maps its own CLI
// errors into.
//
// The core never reads vendor error text. It reacts to the bin and keeps the
// untranslated message beside it for whoever debugs later. Adapters stay small
// because each one only has to know how to sort its own errors; the core only
// has to know the bins.
type FailureKind uint8

const (
	// FailureUnspecified is the zero value and means the adapter failed to sort
	// the error. It is a bug in the adapter, not a category.
	FailureUnspecified FailureKind = iota

	// FailureInvalidInput covers a malformed request: an unknown capability, a
	// payload that does not match the declared schema, a broken settings file.
	FailureInvalidInput

	// FailureNotFound covers "the thing you asked about does not exist here":
	// no match, no symbol, no implementation left after filtering.
	FailureNotFound

	// FailurePermissionDenied covers an action refused on this machine.
	FailurePermissionDenied

	// FailureExternalDenied covers an action refused because it would leave
	// this machine (network, external service).
	//
	// It is deliberately a bin of its own rather than a flavor of
	// FailurePermissionDenied: reaching outside is the only effect no undo can
	// take back, so it has to be visible on its own.
	FailureExternalDenied

	// FailureUnavailable covers a provider that is down or unusable right now.
	// It is the bin that drives fallback.
	FailureUnavailable

	// FailureTimeout covers a provider that took too long.
	FailureTimeout
)

var failureNames = map[FailureKind]string{
	FailureUnspecified:      "unspecified",
	FailureInvalidInput:     "invalid_input",
	FailureNotFound:         "not_found",
	FailurePermissionDenied: "permission_denied",
	FailureExternalDenied:   "external_denied",
	FailureUnavailable:      "unavailable",
	FailureTimeout:          "timeout",
}

func (k FailureKind) String() string {
	if name, ok := failureNames[k]; ok {
		return name
	}
	return "unspecified"
}

// Failure is the error type crossing the contract boundary.
type Failure struct {
	Kind FailureKind
	// Message is Atenea's own wording, in English like every technical artifact.
	Message string
	// Raw is the untranslated output of the underlying CLI or tool, kept so a
	// human can still search for it verbatim. Empty when the failure was raised
	// by the core itself.
	Raw string
}

// Fail builds a Failure with no raw payload.
func Fail(kind FailureKind, format string, args ...any) *Failure {
	return &Failure{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// WithRaw returns a copy of f carrying the untranslated provider output.
func (f *Failure) WithRaw(raw string) *Failure {
	clone := *f
	clone.Raw = raw
	return &clone
}

func (f *Failure) Error() string {
	return f.Kind.String() + ": " + f.Message
}

// KindOf sorts an arbitrary error into a bin. Anything the core did not raise
// itself is unspecified, which is the signal that some adapter did not do its
// job.
func KindOf(err error) FailureKind {
	var f *Failure
	if errors.As(err, &f) {
		return f.Kind
	}
	return FailureUnspecified
}
