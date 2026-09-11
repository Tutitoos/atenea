//nolint:revive // Digest helpers are part of the package's explicit contract.
package remotedevice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

const digestDomainPrefix = "atenea.remote.device.registry/v1"

// Digest returns the domain-separated SHA-256 digest for one exact byte field.
// Multi-field protocol messages use the package's canonical message builder.
func Digest(domain string, field []byte) [32]byte {
	return canonicalDigest(domain, [][]byte{field})
}

func DigestHex(domain string, field []byte) string {
	digest := Digest(domain, field)
	return hex.EncodeToString(digest[:])
}

func canonicalDigest(domain string, fields [][]byte) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(digestDomainPrefix))
	_, _ = hash.Write([]byte{0})
	frame := func(value []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(value)
	}
	frame([]byte(domain))
	for _, field := range fields {
		frame(field)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}
