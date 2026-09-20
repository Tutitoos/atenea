package sshinventory

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProbeResult reports only a client-observed authentication marker or an
// advisory failure. It does not establish the remote machine's identity beyond
// the independently reviewed host key supplied to PrepareDirectProbe.
type ProbeResult struct {
	ClientReportedAuthenticated bool
	Failure                     ProbeFailureKind
}

// ExecuteDirectProbe runs a restricted plan for at most eight seconds. The
// OpenSSH client's own -E log is kept separate from server-supplied banners;
// neither raw channel is returned or persisted. The caller owns plan.Close.
// A plan must not be used or closed concurrently with this call.
func ExecuteDirectProbe(ctx context.Context, sshPath string, plan *ProbePlan) (ProbeResult, error) {
	if ctx == nil || sshPath == "" || plan == nil {
		return ProbeResult{}, ErrUnresolved
	}
	if err := plan.Revalidate(); err != nil {
		return ProbeResult{}, err
	}
	logPath := filepath.Join(plan.root, "client.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ProbeResult{}, err
	}
	if err := logFile.Close(); err != nil {
		return ProbeResult{}, err
	}
	defer func() { _ = os.Remove(logPath) }()
	if err := plan.Revalidate(); err != nil {
		return ProbeResult{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := bounded.Err(); err != nil {
		return ProbeResult{}, err
	}
	args := append([]string{"-v", "-E", logPath}, plan.args...)
	cmd := exec.Command(sshPath, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return ProbeResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case runErr := <-done:
			return inspectProbeLog(logPath, plan.selection, runErr, false)
		case <-ticker.C:
			result, logErr := inspectProbeLog(logPath, plan.selection, nil, false)
			if logErr != nil || result.ClientReportedAuthenticated {
				_ = cmd.Process.Kill()
				<-done
				return result, logErr
			}
		case <-bounded.Done():
			_ = cmd.Process.Kill()
			<-done
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ProbeResult{}, ctx.Err()
			}
			return inspectProbeLog(logPath, plan.selection, nil, true)
		}
	}
}

func inspectProbeLog(path string, selection Selection, runErr error, timedOut bool) (ProbeResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ProbeResult{}, err
	}
	if info.Size() > 32<<10 {
		return ProbeResult{}, errors.New("ssh inventory: diagnostic log exceeds limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ProbeResult{}, err
	}
	if len(data) > 32<<10 {
		return ProbeResult{}, errors.New("ssh inventory: diagnostic log exceeds limit")
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		prefix := "Authenticated to " + selection.HostName + " ("
		if strings.HasPrefix(line, prefix) &&
			strings.HasSuffix(line, "]:"+strconv.Itoa(int(selection.Port))+") using \"publickey\".") {
			return ProbeResult{ClientReportedAuthenticated: true}, nil
		}
	}
	if runErr == nil && !timedOut {
		return ProbeResult{}, nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	failure := ClassifyOpenSSHFailure(exitCode, string(data), timedOut)
	if failure == ProbeFailureAuthRejected && noAvailableExplicitIdentity(selection.IdentityFiles) {
		failure = ProbeFailureAuthRequired
	}
	return ProbeResult{Failure: failure}, nil
}

// noAvailableExplicitIdentity is a conservative diagnostic hint. An existing
// file may still be unreadable or rejected, and dynamic paths are unknown.
// The probe disables the SSH agent and implicit keys, so definitively absent
// selected identity files cannot supply a credential to this probe.
func noAvailableExplicitIdentity(paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, path := range paths {
		if path == "" || strings.ContainsAny(path, "%${}") || (strings.HasPrefix(path, "~") && !strings.HasPrefix(path, "~/")) {
			return false
		}
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return false
			}
			path = filepath.Join(home, path[2:])
		}
		_, err := os.Stat(path)
		if !os.IsNotExist(err) {
			return false
		}
	}
	return true
}
