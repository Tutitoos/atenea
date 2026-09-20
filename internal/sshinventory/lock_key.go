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

// ProvisionalDirectLockKey groups aliases that resolve to the same direct
// hostname, port and account. It is only a pre-authentication serialization
// key: DNS, host keys and credentials are not verified here. Revalidate the
// selection before use and never use this key as authorization or trust.
func ProvisionalDirectLockKey(selection Selection) (string, error) {
	if selection.Snapshot == "" || selection.Port == 0 ||
		!safeHostArgument(selection.HostName) || !safeAccountArgument(selection.User) {
		return "", ErrUnresolved
	}
	if selection.ProxyJump != "" || selection.ProxyCommand != "" {
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
