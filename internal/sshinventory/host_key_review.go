package sshinventory

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ErrFingerprintMismatch means the supplied host key differs from the
// fingerprint that the caller claims was checked through another channel.
var ErrFingerprintMismatch = errors.New("ssh inventory: host key fingerprint mismatch")

// DirectHostKeyEntry is a single plain ED25519 known_hosts entry bound to a
// config snapshot. It is not persisted or proof that a fingerprint was
// independently verified; the caller owns that review and user confirmation.
type DirectHostKeyEntry struct {
	line     string
	snapshot string
	alias    string
	account  string
	keyHash  [sha256.Size]byte
}

// KnownHostsLine returns a copy suitable for a temporary known_hosts file.
func (e DirectHostKeyEntry) KnownHostsLine() []byte { return []byte(e.line) }

// Snapshot returns the config fingerprint that this entry was matched to.
func (e DirectHostKeyEntry) Snapshot() string { return e.snapshot }

// ConfirmedDirectHostKey is an in-memory approval for one matched direct key.
// Only a caller handling an explicit user confirmation should construct it;
// the library cannot establish who supplied the confirmation fingerprint.
type ConfirmedDirectHostKey struct{ entry DirectHostKeyEntry }

// ConfirmDirectED25519HostKey checks a user-confirmed SHA256 fingerprint
// against a previously matched entry and the current selected config. It
// neither writes a trust store nor connects to the target.
func ConfirmDirectED25519HostKey(userConfig, systemConfig string, selected Selection, entry DirectHostKeyEntry, confirmedSHA256 string) (ConfirmedDirectHostKey, error) {
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, selected, entry); err != nil {
		return ConfirmedDirectHostKey{}, err
	}
	if !strings.HasPrefix(confirmedSHA256, "SHA256:") {
		return ConfirmedDirectHostKey{}, ErrUnresolved
	}
	confirmed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(confirmedSHA256, "SHA256:"))
	if err != nil || len(confirmed) != sha256.Size {
		return ConfirmedDirectHostKey{}, ErrUnresolved
	}
	if subtle.ConstantTimeCompare(confirmed, entry.keyHash[:]) != 1 {
		return ConfirmedDirectHostKey{}, ErrFingerprintMismatch
	}
	return ConfirmedDirectHostKey{entry: entry}, nil
}

// PrepareConfirmedDirectProbe uses only the exact key in a current in-memory
// confirmation. It still performs a noninteractive diagnostic, not enrollment.
func PrepareConfirmedDirectProbe(userConfig, systemConfig string, selected Selection, confirmed ConfirmedDirectHostKey) (*ProbePlan, error) {
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, selected, confirmed.entry); err != nil {
		return nil, err
	}
	return PrepareDirectProbe(userConfig, systemConfig, selected, confirmed.entry.KnownHostsLine())
}

func validateDirectHostKeyEntry(userConfig, systemConfig string, selected Selection, entry DirectHostKeyEntry) error {
	if err := RevalidateSelection(userConfig, systemConfig, selected); err != nil {
		return err
	}
	if entry.line == "" || entry.snapshot != selected.Snapshot || entry.alias != selected.Alias || entry.account != selected.User {
		return ErrChanged
	}
	return nil
}

// MatchDirectED25519HostKey checks a candidate public key against a SHA256
// fingerprint supplied from an independent channel. It accepts only a plain
// ED25519 key, an exact direct host/port or HostKeyAlias binding, and current
// static config. It performs no network call and writes no trust store.
func MatchDirectED25519HostKey(userConfig, systemConfig string, selected Selection, candidatePublic, independentSHA256 string) (DirectHostKeyEntry, error) {
	if err := RevalidateSelection(userConfig, systemConfig, selected); err != nil {
		return DirectHostKeyEntry{}, err
	}
	if selected.Port == 0 || !safeHostArgument(selected.HostName) || !safeAccountArgument(selected.User) ||
		(selected.HostKeyAlias != "" && !safeHostArgument(selected.HostKeyAlias)) {
		return DirectHostKeyEntry{}, ErrUnresolved
	}
	if activeProxyRoute(selected) {
		return DirectHostKeyEntry{}, ErrProbeUnsupported
	}
	host, err := directKnownHostToken(selected)
	if err != nil {
		return DirectHostKeyEntry{}, err
	}
	if len(candidatePublic) == 0 || len(candidatePublic) > 8<<10 || strings.ContainsAny(candidatePublic, "\r\n\x00") {
		return DirectHostKeyEntry{}, ErrUnresolved
	}
	fields := strings.Fields(candidatePublic)
	if len(fields) != 2 || fields[0] != "ssh-ed25519" {
		return DirectHostKeyEntry{}, ErrProbeUnsupported
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || !validED25519Blob(blob) {
		return DirectHostKeyEntry{}, ErrUnresolved
	}
	if !strings.HasPrefix(independentSHA256, "SHA256:") {
		return DirectHostKeyEntry{}, ErrUnresolved
	}
	expected, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(independentSHA256, "SHA256:"))
	if err != nil || len(expected) != sha256.Size {
		return DirectHostKeyEntry{}, ErrUnresolved
	}
	actual := sha256.Sum256(blob)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return DirectHostKeyEntry{}, ErrFingerprintMismatch
	}
	line := fmt.Sprintf("%s ssh-ed25519 %s\n", host, base64.StdEncoding.EncodeToString(blob))
	if err := RevalidateSelection(userConfig, systemConfig, selected); err != nil {
		return DirectHostKeyEntry{}, err
	}
	return DirectHostKeyEntry{line: line, snapshot: selected.Snapshot, alias: selected.Alias, account: selected.User, keyHash: actual}, nil
}

func directKnownHostToken(selected Selection) (string, error) {
	host, err := directKnownHostName(selected.HostName)
	if err != nil {
		return "", err
	}
	if selected.HostKeyAlias != "" {
		if !safeHostArgument(selected.HostKeyAlias) {
			return "", ErrUnresolved
		}
		return selected.HostKeyAlias, nil
	}
	if selected.Port == 0 {
		return "", ErrUnresolved
	}
	if selected.Port != 22 {
		host = "[" + host + "]:" + strconv.Itoa(int(selected.Port))
	}
	return host, nil
}

func directKnownHostName(value string) (string, error) {
	if !safeHostArgument(value) {
		return "", ErrUnresolved
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		address := value[1 : len(value)-1]
		if _, err := netip.ParseAddr(address); err == nil {
			return address, nil
		}
		return "", ErrUnresolved
	}
	if strings.ContainsAny(value, "[]") {
		return "", ErrUnresolved
	}
	return value, nil
}

func validED25519Blob(blob []byte) bool {
	if len(blob) != 4+len("ssh-ed25519")+4+32 {
		return false
	}
	algorithmLength := int(binary.BigEndian.Uint32(blob[:4]))
	if algorithmLength != len("ssh-ed25519") || string(blob[4:4+algorithmLength]) != "ssh-ed25519" {
		return false
	}
	keyLengthStart := 4 + algorithmLength
	return binary.BigEndian.Uint32(blob[keyLengthStart:keyLengthStart+4]) == 32
}
