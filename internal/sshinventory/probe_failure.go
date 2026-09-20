package sshinventory

import "strings"

// ProbeFailureKind is an advisory classification of a failed OpenSSH process.
// stderr may contain server-controlled banner text. Never use this hint to
// enroll a host key, release credentials, or assert authenticated connectivity.
type ProbeFailureKind string

// ProbeFailureKind values are diagnostic hints only; unknown includes a
// zero exit because this classifier does not establish authentication.
const (
	ProbeFailureUnknown        ProbeFailureKind = "unknown"
	ProbeFailureTimeout        ProbeFailureKind = "timeout"
	ProbeFailureDNS            ProbeFailureKind = "dns_failure"
	ProbeFailureRoute          ProbeFailureKind = "route_failure"
	ProbeFailureAuthRequired   ProbeFailureKind = "authentication_required"
	ProbeFailureAuthRejected   ProbeFailureKind = "authentication_rejected"
	ProbeFailureHostKeyUnknown ProbeFailureKind = "host_key_unknown"
	ProbeFailureHostKeyChanged ProbeFailureKind = "host_key_changed"
	ProbeFailureHostKeyOther   ProbeFailureKind = "host_key_rejected"
)

// ClassifyOpenSSHFailure interprets bounded English OpenSSH diagnostics from
// a failed, direct diagnostic attempt. It does not run SSH or return raw logs.
// The caller must set timedOut from its own process deadline; exitCode 255 is
// required for stderr-based hints. An exit code of zero is never treated as
// authenticated connectivity by this function.
func ClassifyOpenSSHFailure(exitCode int, stderr string, timedOut bool) ProbeFailureKind {
	if timedOut {
		return ProbeFailureTimeout
	}
	if exitCode != 255 || len(stderr) > 32<<10 {
		return ProbeFailureUnknown
	}
	message := strings.ToLower(stderr)
	switch {
	case strings.Contains(message, "remote host identification has changed") && strings.Contains(message, "host key verification failed"):
		return ProbeFailureHostKeyChanged
	case strings.Contains(message, "host key is known for") && strings.Contains(message, "host key verification failed"):
		return ProbeFailureHostKeyUnknown
	case strings.Contains(message, "host key verification failed"):
		return ProbeFailureHostKeyOther
	case strings.Contains(message, "could not resolve hostname"):
		return ProbeFailureDNS
	case strings.Contains(message, "connection timed out"), strings.Contains(message, "operation timed out"):
		return ProbeFailureTimeout
	case strings.Contains(message, "no route to host"), strings.Contains(message, "network is unreachable"), strings.Contains(message, "connection refused"):
		return ProbeFailureRoute
	case strings.Contains(message, "permission denied ("), strings.Contains(message, "too many authentication failures"):
		return ProbeFailureAuthRejected
	default:
		return ProbeFailureUnknown
	}
}
