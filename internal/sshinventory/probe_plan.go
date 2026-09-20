package sshinventory

import (
	"errors"
	"io"
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
	rootInfo     os.FileInfo
	userConfig   string
	systemConfig string
	selection    Selection
	jump         *Selection
	trustCheck   func() error
	trustLock    func() (func(), error)
}

// Arguments returns a copy of the restricted OpenSSH arguments. Changing the
// returned slice cannot modify this plan. It is empty after Close and for an
// enrolled plan, whose trust must be rechecked under the executor's lock.
func (p *ProbePlan) Arguments() []string {
	if p == nil || p.root == "" || p.trustCheck != nil {
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
	if !privateProbeDirectory(p.root, p.rootInfo) {
		return ErrProbeUnsupported
	}
	if err := RevalidateSelection(p.userConfig, p.systemConfig, p.selection); err != nil {
		return err
	}
	if p.jump != nil {
		if err := RevalidateSelection(p.userConfig, p.systemConfig, *p.jump); err != nil {
			return err
		}
	}
	if p.trustCheck != nil {
		return p.trustCheck()
	}
	return nil
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
	if activeProxyRoute(selection) {
		return nil, ErrProbeUnsupported // Preserve, then review the configured route separately.
	}
	if len(knownHostsSnapshot) == 0 || len(knownHostsSnapshot) > 1<<20 {
		return nil, ErrUnresolved
	}
	hasExplicitIdentity := false
	for _, path := range selection.IdentityFiles {
		if path == "" || strings.ContainsAny(path, "\x00\r\n") {
			return nil, ErrUnresolved
		}
		if path != "none" {
			hasExplicitIdentity = true
		}
	}
	root, err := newPrivateProbeDirectory()
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	rootInfo, err := os.Lstat(root)
	if err != nil || !privateProbeDirectory(root, rootInfo) {
		cleanup()
		return nil, ErrProbeUnsupported
	}
	config := filepath.Join(root, "config")
	knownHosts := filepath.Join(root, "known_hosts")
	if err := createPrivateProbeFile(config, nil); err != nil {
		cleanup()
		return nil, err
	}
	if err := createPrivateProbeFile(knownHosts, knownHostsSnapshot); err != nil {
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
		"-o", "AddKeysToAgent=no",
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
	if !hasExplicitIdentity {
		args = append(args, "-o", "PubkeyAuthentication=no")
	}
	for _, identity := range selection.IdentityFiles {
		if identity != "none" {
			args = append(args, "-i", identity)
		}
	}
	args = append(args, selection.HostName)
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		cleanup()
		return nil, err
	}
	selection.IdentityFiles = append([]string(nil), selection.IdentityFiles...)
	selection.Sources = append([]Diagnostic(nil), selection.Sources...)
	return &ProbePlan{args: args, root: root, rootInfo: rootInfo, userConfig: userConfig, systemConfig: systemConfig, selection: selection}, nil
}

func createPrivateProbeFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if err := preparePrivateTrustFile(path, file); err != nil {
		remove()
		return err
	}
	info, err := file.Stat()
	if err != nil || !privateTrustFile(info, file) {
		remove()
		return ErrProbeUnsupported
	}
	if len(data) != 0 {
		n, err := file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			remove()
			return err
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func activeProxyRoute(selection Selection) bool {
	return (selection.ProxyJump != "" && !strings.EqualFold(selection.ProxyJump, "none")) ||
		(selection.ProxyCommand != "" && !strings.EqualFold(selection.ProxyCommand, "none"))
}

// Close removes the temporary configuration and known-hosts files.
func (p *ProbePlan) Close() error {
	if p == nil || p.root == "" {
		return nil
	}
	root := p.root
	rootInfo := p.rootInfo
	p.root = ""
	p.rootInfo = nil
	p.args = nil
	p.userConfig = ""
	p.systemConfig = ""
	p.selection = Selection{}
	p.jump = nil
	p.trustCheck = nil
	p.trustLock = nil
	if !privateProbeDirectory(root, rootInfo) {
		return ErrProbeUnsupported
	}
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
	if value == "" || value[0] == '-' || strings.ContainsAny(value, ":/ \t\r\n\x00") {
		return false
	}
	for _, c := range value {
		if c < 33 || c == 127 {
			return false
		}
	}
	return true
}
