//go:build windows

package sshinventory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

func newPrivateProbeDirectory() (string, error) {
	parent, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	for range 16 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		path := filepath.Join(parent, "atenea-ssh-probe-"+hex.EncodeToString(nonce[:]))
		if err := createPrivateTrustDirectory(path); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		return path, nil
	}
	return "", ErrProbeUnsupported
}

func privateProbeDirectory(path string, initial os.FileInfo) bool {
	if initial == nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(initial, current) && privateTrustDirectory(path, current)
}
