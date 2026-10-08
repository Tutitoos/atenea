package sshinventory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"net/netip"
	"strconv"
	"strings"
)

// MatchedDirectAccountKey groups aliases by a fingerprint-matched ED25519
// host key and account after the selected config is revalidated. It is an
// opaque serialization key, not proof that a fingerprint was independently
// reviewed or that a physical device is unique: cloned hosts may share keys.
// Callers must still require explicit trust confirmation and invalidate cached
// authorization when the selection or credential changes.
func MatchedDirectAccountKey(userConfig, systemConfig string, selection Selection, entry DirectHostKeyEntry) (string, error) {
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, selection, entry); err != nil {
		return "", err
	}
	h := sha256.New()
	writeLockField(h, "atenea-ed25519-account-v1")
	writeLockField(h, string(entry.keyHash[:]))
	writeLockField(h, selection.User)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ProvisionalDirectLockKey groups aliases that resolve to the same direct
// hostname, port and account. It is only a pre-authentication serialization
// key: DNS, host keys and credentials are not verified here. Revalidate the
// selection before use and never use this key as authorization or trust.
func ProvisionalDirectLockKey(selection Selection) (string, error) {
	if selection.Snapshot == "" || selection.Port == 0 ||
		!safeHostArgument(selection.HostName) || !safeAccountArgument(selection.User) {
		return "", ErrUnresolved
	}
	if activeProxyRoute(selection) {
		return "", ErrProbeUnsupported
	}
	host := strings.TrimSuffix(strings.ToLower(selection.HostName), ".")
	if strings.HasPrefix(host, "[") != strings.HasSuffix(host, "]") {
		return "", ErrUnresolved
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if _, err := netip.ParseAddr(host); err != nil {
			return "", ErrUnresolved
		}
	}
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.String()
	}
	if host == "" {
		return "", ErrUnresolved
	}
	h := sha256.New()
	writeLockField(h, "atenea-direct-account-v1")
	writeLockField(h, host)
	writeLockField(h, strconv.Itoa(int(selection.Port)))
	writeLockField(h, selection.User)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLockField(h hash.Hash, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}
