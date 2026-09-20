package sshinventory

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// PrepareSingleJumpProbe prepares a noninteractive probe through one explicitly
// listed ProxyJump alias. Both ED25519 pins must be supplied from independent
// review; this method does not enroll either key. This narrow route is
// available on Unix only until child-process containment is validated on
// Windows. Arbitrary ProxyCommand and multi-hop routes remain unsupported.
func PrepareSingleJumpProbe(userConfig, systemConfig string, target, jump Selection, knownHostsSnapshot []byte) (*ProbePlan, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, ErrProbeUnsupported
	}
	if err := RevalidateSelection(userConfig, systemConfig, target); err != nil {
		return nil, err
	}
	if err := RevalidateSelection(userConfig, systemConfig, jump); err != nil {
		return nil, err
	}
	if target.Snapshot == "" || target.Snapshot != jump.Snapshot || target.Alias == jump.Alias ||
		!safeJumpAlias(jump.Alias) || target.ProxyJump != jump.Alias ||
		(target.ProxyCommand != "" && !strings.EqualFold(target.ProxyCommand, "none")) || activeProxyRoute(jump) {
		return nil, ErrProbeUnsupported
	}
	for _, selection := range []Selection{target, jump} {
		if selection.Port == 0 || !safeHostArgument(selection.HostName) || !safeAccountArgument(selection.User) ||
			(selection.HostKeyAlias != "" && !safeHostArgument(selection.HostKeyAlias)) {
			return nil, ErrUnresolved
		}
	}
	targetToken, err := directKnownHostToken(target)
	if err != nil {
		return nil, err
	}
	jumpToken, err := directKnownHostToken(jump)
	if err != nil {
		return nil, err
	}
	if targetToken == jumpToken && (target.HostName != jump.HostName || target.Port != jump.Port) {
		return nil, ErrProbeUnsupported
	}
	if err := validateJumpPins(knownHostsSnapshot, targetToken, jumpToken); err != nil {
		return nil, err
	}
	for _, identity := range jump.IdentityFiles {
		if identity == "none" {
			continue
		}
		if !filepath.IsAbs(identity) || !safeProbeConfigAtom(identity) {
			return nil, ErrProbeUnsupported
		}
	}
	for _, identity := range target.IdentityFiles {
		if identity == "" || strings.ContainsAny(identity, "\x00\r\n") {
			return nil, ErrUnresolved
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
	globalHosts := filepath.Join(root, "global_known_hosts")
	if !safeProbeConfigAtom(knownHosts) || !safeProbeConfigAtom(globalHosts) || !safeProbeConfigAtom(jump.User) {
		cleanup()
		return nil, ErrProbeUnsupported
	}
	if err := createPrivateProbeFile(knownHosts, knownHostsSnapshot); err != nil {
		cleanup()
		return nil, err
	}
	if err := createPrivateProbeFile(globalHosts, nil); err != nil {
		cleanup()
		return nil, err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Host *\n BatchMode yes\n CanonicalizeHostname no\n CheckHostIP no\n ClearAllForwardings yes\n ConnectionAttempts 1\n ConnectTimeout 5\n ControlMaster no\n ControlPath none\n ControlPersist no\n ForwardAgent no\n ForwardX11 no\n AddKeysToAgent no\n IdentitiesOnly yes\n IdentityAgent none\n IdentityFile none\n PreferredAuthentications publickey\n PermitLocalCommand no\n RequestTTY no\n StrictHostKeyChecking yes\n UpdateHostKeys no\n UserKnownHostsFile %s\n GlobalKnownHostsFile %s\n", knownHosts, globalHosts)
	fmt.Fprintf(&text, "Host %s\n HostName %s\n User %s\n Port %d\n", jump.Alias, jump.HostName, jump.User, jump.Port)
	if jump.HostKeyAlias != "" {
		fmt.Fprintf(&text, " HostKeyAlias %s\n", jump.HostKeyAlias)
	}
	for _, identity := range jump.IdentityFiles {
		if identity != "none" {
			fmt.Fprintf(&text, " IdentityFile %s\n", identity)
		}
	}
	if err := createPrivateProbeFile(config, []byte(text.String())); err != nil {
		cleanup()
		return nil, err
	}
	args := []string{"-F", config, "-N", "-T", "-n", "-J", jump.Alias,
		"-o", "BatchMode=yes", "-o", "IdentityFile=none", "-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none", "-o", "AddKeysToAgent=no", "-o", "ClearAllForwardings=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=" + globalHosts,
		"-p", strconv.Itoa(int(target.Port)), "-l", target.User}
	if target.HostKeyAlias != "" {
		args = append(args, "-o", "HostKeyAlias="+target.HostKeyAlias)
	}
	if noAvailableExplicitIdentity(target.IdentityFiles) {
		args = append(args, "-o", "PubkeyAuthentication=no")
	}
	for _, identity := range target.IdentityFiles {
		if identity != "none" {
			args = append(args, "-i", identity)
		}
	}
	args = append(args, target.HostName)
	if err := RevalidateSelection(userConfig, systemConfig, target); err != nil {
		cleanup()
		return nil, err
	}
	if err := RevalidateSelection(userConfig, systemConfig, jump); err != nil {
		cleanup()
		return nil, err
	}
	target.IdentityFiles = append([]string(nil), target.IdentityFiles...)
	target.Sources = append([]Diagnostic(nil), target.Sources...)
	jump.IdentityFiles = append([]string(nil), jump.IdentityFiles...)
	jump.Sources = append([]Diagnostic(nil), jump.Sources...)
	return &ProbePlan{args: args, root: root, rootInfo: rootInfo, userConfig: userConfig, systemConfig: systemConfig, selection: target, jump: &jump}, nil
}

func safeJumpAlias(alias string) bool {
	if alias == "" || alias[0] == '-' {
		return false
	}
	for _, c := range alias {
		allowed := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || strings.ContainsRune("._-", c)
		if !allowed {
			return false
		}
	}
	return true
}

func validateSingleJumpRoute(userConfig, systemConfig string, target, jump Selection) error {
	if err := RevalidateSelection(userConfig, systemConfig, target); err != nil {
		return err
	}
	if err := RevalidateSelection(userConfig, systemConfig, jump); err != nil {
		return err
	}
	if target.Snapshot == "" || target.Snapshot != jump.Snapshot || target.Alias == jump.Alias ||
		!safeJumpAlias(jump.Alias) || target.ProxyJump != jump.Alias ||
		(target.ProxyCommand != "" && !strings.EqualFold(target.ProxyCommand, "none")) || activeProxyRoute(jump) {
		return ErrProbeUnsupported
	}
	targetToken, err := directKnownHostToken(target)
	if err != nil {
		return err
	}
	jumpToken, err := directKnownHostToken(jump)
	if err != nil {
		return err
	}
	if targetToken == jumpToken && (target.HostName != jump.HostName || target.Port != jump.Port) {
		return ErrProbeUnsupported
	}
	return nil
}

// PrepareConfirmedSingleJumpProbe uses two independently confirmed host keys
// bound to the exact destination and gateway selections. It creates no durable
// trust state and still requires explicit review by the caller.
func PrepareConfirmedSingleJumpProbe(userConfig, systemConfig string, target, jump Selection, targetKey, jumpKey ConfirmedDirectHostKey) (*ProbePlan, error) {
	if err := validateSingleJumpRoute(userConfig, systemConfig, target, jump); err != nil {
		return nil, err
	}
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, target, targetKey.entry); err != nil {
		return nil, err
	}
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, jump, jumpKey.entry); err != nil {
		return nil, err
	}
	return PrepareSingleJumpProbe(userConfig, systemConfig, target, jump,
		append(targetKey.entry.KnownHostsLine(), jumpKey.entry.KnownHostsLine()...))
}

func safeProbeConfigAtom(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \t\r\n\x00#\"'\\%${}")
}

func validateJumpPins(data []byte, targetToken, jumpToken string) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return ErrUnresolved
	}
	foundTarget, foundJump := false, false
	seen := make(map[string]bool, 2)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "ssh-ed25519" ||
			(fields[0] != targetToken && fields[0] != jumpToken) {
			return ErrProbeUnsupported
		}
		if seen[fields[0]] {
			return ErrProbeUnsupported
		}
		seen[fields[0]] = true
		blob, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil || !validED25519Blob(blob) {
			return ErrUnresolved
		}
		foundTarget = foundTarget || fields[0] == targetToken
		foundJump = foundJump || fields[0] == jumpToken
	}
	if !foundTarget || !foundJump {
		return ErrProbeUnsupported
	}
	return nil
}
