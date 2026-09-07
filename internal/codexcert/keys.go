package codexcert

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateSigningKey loads a local raw Ed25519 key or creates one with
// owner-only permissions. The private key never enters a certificate.
func LoadOrCreateSigningKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	load := func() (ed25519.PrivateKey, ed25519.PublicKey, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, nil, errors.New("codex certification signing key must be a regular 0600 file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, nil, errors.New("invalid codex certification signing key")
		}
		privateKey := ed25519.PrivateKey(raw)
		return privateKey, privateKey.Public().(ed25519.PublicKey), nil
	}
	if privateKey, publicKey, err := load(); err == nil {
		return privateKey, publicKey, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return load()
	}
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.WriteString(base64.StdEncoding.EncodeToString(privateKey)); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err = file.Close(); err != nil {
		return nil, nil, err
	}
	ok = true
	return privateKey, publicKey, nil
}

// EncodePublicKey serializes a pinned release-verification key.
func EncodePublicKey(key ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(key) }

// DecodePublicKey parses a pinned release-verification key.
func DecodePublicKey(data []byte) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Codex certification public key")
	}
	return ed25519.PublicKey(raw), nil
}
