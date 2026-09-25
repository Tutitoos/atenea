package sshinventory

import (
	"strings"
	"testing"
)

func TestClassifyOpenSSHFailure(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		timedOut bool
		want     ProbeFailureKind
	}{
		{"changed", 255, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost key verification failed.", false, ProbeFailureHostKeyChanged},
		{"changed marker alone", 255, "REMOTE HOST IDENTIFICATION HAS CHANGED!", false, ProbeFailureUnknown},
		{"unknown", 255, "No ED25519 host key is known for [127.0.0.1]:2222\nHost key verification failed.", false, ProbeFailureHostKeyUnknown},
		{"key rejected other", 255, "Host key verification failed.", false, ProbeFailureHostKeyOther},
		{"dns", 255, "ssh: Could not resolve hostname fixture.invalid: nodename nor servname provided", false, ProbeFailureDNS},
		{"route", 255, "ssh: connect to host fixture.invalid port 22: Connection refused", false, ProbeFailureRoute},
		{"timeout text", 255, "ssh: connect to host fixture.invalid port 22: Operation timed out", false, ProbeFailureTimeout},
		{"deadline", -1, "", true, ProbeFailureTimeout},
		{"auth rejected", 255, "person@fixture.invalid: Permission denied (publickey).", false, ProbeFailureAuthRejected},
		{"other exit", 1, "Permission denied (publickey).", false, ProbeFailureUnknown},
		{"zero exit", 0, "Authenticated to fixture.invalid", false, ProbeFailureUnknown},
		{"oversized", 255, strings.Repeat("x", 33<<10) + "Permission denied (publickey).", false, ProbeFailureUnknown},
		{"unrecognized", 255, "Connection closed by remote host", false, ProbeFailureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyOpenSSHFailure(tt.exitCode, tt.stderr, tt.timedOut); got != tt.want {
				t.Fatalf("classification = %q, want %q", got, tt.want)
			}
		})
	}
}
