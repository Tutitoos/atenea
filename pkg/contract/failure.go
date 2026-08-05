package contract

import (
	"context"
	"errors"
	"fmt"
	"time"
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

	// FailureCanceled covers a call stopped from this side: the user pressed
	// ctrl-c, or a caller dropped the context.
	//
	// It is the one bin that says nothing about the provider, and every
	// consumer has to treat it that way. It is not evidence of a fault, so it
	// never reaches the health record. It is not a measurement of what a tool
	// costs, so it never reaches the base -- the clock was timing how long
	// somebody waited before changing their mind, and a provider interrupted
	// at two seconds is not a provider that took five minutes.
	FailureCanceled
)

var failureNames = map[FailureKind]string{
	FailureUnspecified:      "unspecified",
	FailureInvalidInput:     "invalid_input",
	FailureNotFound:         "not_found",
	FailurePermissionDenied: "permission_denied",
	FailureExternalDenied:   "external_denied",
	FailureUnavailable:      "unavailable",
	FailureTimeout:          "timeout",
	FailureCanceled:         "canceled",
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

// RawOf recovers the untranslated provider text behind an arbitrary error, or
// the empty string when there is none -- either because the core raised the
// error itself, or because whatever adapter raised it never attached one.
//
// It exists for the same reason KindOf does: everything downstream of an
// adapter call works with a plain error, and this is the one place that
// reaches back into the concrete type to recover what a human needs. Every
// caller that keeps a failure around for later -- a step result, a
// measurement, a checkpoint -- calls this once here rather than each
// learning to type-assert its own copy.
func RawOf(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Raw
	}
	return ""
}

// StopKind sorts a context error into its bin.
//
// The two cases look identical at the call site -- the work did not finish and
// ctx.Err() is non-nil -- and they mean opposite things: a deadline is
// something running out of the time it was given, a cancellation is somebody
// changing their mind. Filing the second as the first is how a user who
// pressed ctrl-c after two seconds gets told a provider took five minutes, and
// how that provider collects a fault it did not earn.
//
// The distinction survives a derived context with its own timeout on it: when
// that deadline fires the error is DeadlineExceeded, and when the caller above
// goes away it is Canceled.
func StopKind(ctxErr error) FailureKind {
	if errors.Is(ctxErr, context.Canceled) {
		return FailureCanceled
	}
	return FailureTimeout
}

// Stopped is StopKind with the sentence an adapter should say beside it. The
// core has its own wording and uses StopKind directly: a run is not a
// provider, and telling a user their run "was stopped before it answered"
// would be Atenea talking about itself in the third person.
func Stopped(ctxErr error, provider string, limit time.Duration) *Failure {
	if StopKind(ctxErr) == FailureCanceled {
		return Fail(FailureCanceled, "%s was stopped before it answered", provider)
	}
	return Fail(FailureTimeout, "%s took longer than %s", provider, limit)
}
