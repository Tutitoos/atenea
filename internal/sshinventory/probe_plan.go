package sshinventory

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrProbeUnsupported means a selected route cannot yet be probed without changing its semantics.
var ErrProbeUnsupported = errors.New("ssh inventory: diagnostic probe unsupported for this route")

// ProbePlan contains a restricted OpenSSH command line and temporary files.
// Preparing it never connects. Close removes the private files after use.
type ProbePlan struct {
	args         []string
	root         string
	userConfig   string
	systemConfig string
	selection    Selection
}

// Arguments returns a copy of the restricted OpenSSH arguments. Changing the
// returned slice cannot modify this plan. It is empty after Close.
func (p *ProbePlan) Arguments() []string {
	if p == nil || p.root == "" {
		return nil
	}
	return append([]string(nil), p.args...)
}

// Snapshot returns the config digest captured when this plan was built.
func (p *ProbePlan) Snapshot() string {
	if p == nil || p.root == "" {
		return ""
	}
	return p.selection.Snapshot
}

// Revalidate rereads the original config roots and refuses a stale or closed
// plan. An executor must call it immediately before starting OpenSSH.
func (p *ProbePlan) Revalidate() error {
	if p == nil || p.root == "" {
		return ErrUnresolved
	}
	return RevalidateSelection(p.userConfig, p.systemConfig, p.selection)
}

// PrepareDirectProbe builds a direct-route diagnostic plan. The caller must
// supply known_hosts bytes from an independently reviewed source. It rechecks
// the selected config roots before and after writing temporary files. The
// caller must recheck again immediately before running the plan. StrictHostKeyChecking
// refuses unknown or changed keys; this function never enrolls a key.
func PrepareDirectProbe(userConfig, systemConfig string, selection Selection, knownHostsSnapshot []byte) (*ProbePlan, error) {
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return nil, err
	}
	if selection.Snapshot == "" || selection.User == "" || selection.Port == 0 ||
		!safeHostArgument(selection.HostName) || !safeAccountArgument(selection.User) ||
		(selection.HostKeyAlias != "" && !safeHostArgument(selection.HostKeyAlias)) {
		return nil, ErrUnresolved
	}
	if selection.ProxyJump != "" || selection.ProxyCommand != "" {
		return nil, ErrProbeUnsupported // Preserve, then review the configured route separately.
	}
	if len(knownHostsSnapshot) == 0 || len(knownHostsSnapshot) > 1<<20 {
		return nil, ErrUnresolved
	}
	for _, path := range selection.IdentityFiles {
		if path == "" || strings.EqualFold(path, "none") || strings.ContainsAny(path, "\x00\r\n") {
			return nil, ErrUnresolved
		}
	}
	root, err := os.MkdirTemp("", "atenea-ssh-probe-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanup()
		return nil, err
	}
	config := filepath.Join(root, "config")
	knownHosts := filepath.Join(root, "known_hosts")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.WriteFile(knownHosts, knownHostsSnapshot, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	args := []string{
		"-F", config, "-N", "-T", "-n",
		"-o", "BatchMode=yes",
		"-o", "CanonicalizeHostname=no",
		"-o", "CheckHostIP=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ConnectionAttempts=1",
		"-o", "ConnectTimeout=5",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ControlPersist=no",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "IdentityFile=none",
		"-o", "PreferredAuthentications=publickey",
		"-o", "PermitLocalCommand=no",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=" + config,
		"-p", strconv.Itoa(int(selection.Port)), "-l", selection.User,
	}
	if selection.HostKeyAlias != "" {
		args = append(args, "-o", "HostKeyAlias="+selection.HostKeyAlias)
	}
	if len(selection.IdentityFiles) == 0 {
		args = append(args, "-o", "PubkeyAuthentication=no")
	}
	for _, identity := range selection.IdentityFiles {
		args = append(args, "-i", identity)
	}
	args = append(args, selection.HostName)
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		cleanup()
		return nil, err
	}
	selection.IdentityFiles = append([]string(nil), selection.IdentityFiles...)
	selection.Sources = append([]Diagnostic(nil), selection.Sources...)
	return &ProbePlan{args: args, root: root, userConfig: userConfig, systemConfig: systemConfig, selection: selection}, nil
}

// Close removes the temporary configuration and known-hosts files.
func (p *ProbePlan) Close() error {
	if p == nil || p.root == "" {
		return nil
	}
	root := p.root
	p.root = ""
	p.args = nil
	p.userConfig = ""
	p.systemConfig = ""
	p.selection = Selection{}
	return os.RemoveAll(root)
}

func safeHostArgument(value string) bool {
	if value == "" || value[0] == '-' {
		return false
	}
	for _, c := range value {
		allowed := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || strings.ContainsRune("._:-[]", c)
		if !allowed {
			return false
		}
	}
	return true
}

func safeAccountArgument(value string) bool {
	if value == "" || value[0] == '-' || strings.ContainsAny(value, ":/\\ \t\r\n\x00") {
		return false
	}
	for _, c := range value {
		if c < 33 || c == 127 {
			return false
		}
	}
	return true
}
