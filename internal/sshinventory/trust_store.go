package sshinventory

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ErrTrustNotEnrolled means there is no approved pin for this exact selected
// config, account, port and known-hosts name in the app's private store.
var ErrTrustNotEnrolled = errors.New("ssh inventory: selected host key is not enrolled")

// ErrTrustBusy means another local operation holds this pin's mutation lock.
var ErrTrustBusy = errors.New("ssh inventory: selected host key is being updated")

// DirectTrustStore keeps one plain ED25519 pin per selected direct host in an
// app-owned directory. Existing pins are never replaced automatically.
// Native Windows ACL validation is still pending, so this store fails closed
// there rather than relying on Unix permission bits.
type DirectTrustStore struct {
	root     string
	rootInfo os.FileInfo
}

// OpenPrivateDirectTrustStore creates or opens an app-owned directory whose
// parent is trusted by the caller. It refuses symlinks and group/world access.
func OpenPrivateDirectTrustStore(root string) (*DirectTrustStore, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, ErrProbeUnsupported
	}
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, ErrUnresolved
	}
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	store := &DirectTrustStore{root: root, rootInfo: info}
	if err := store.checkRoot(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *DirectTrustStore) checkRoot() error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	return root.Close()
}

func (s *DirectTrustStore) openRoot() (*os.Root, error) {
	if s == nil || s.root == "" || s.rootInfo == nil {
		return nil, ErrUnresolved
	}
	info, err := os.Lstat(s.root)
	if err != nil {
		return nil, err
	}
	if !privateTrustDirectory(info) || !os.SameFile(s.rootInfo, info) {
		return nil, ErrProbeUnsupported
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !privateTrustDirectory(opened) || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, ErrProbeUnsupported
	}
	return root, nil
}

func privateTrustDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0
}

func (s *DirectTrustStore) fileFor(selection Selection) (string, string, error) {
	if selection.Snapshot == "" || !safeAccountArgument(selection.User) {
		return "", "", ErrUnresolved
	}
	host, err := directKnownHostToken(selection)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	writeLockField(h, "atenea-direct-trust-v1")
	writeLockField(h, selection.Snapshot)
	writeLockField(h, host)
	writeLockField(h, strconv.Itoa(int(selection.Port)))
	writeLockField(h, selection.User)
	return filepath.Join(s.root, hex.EncodeToString(h.Sum(nil))+".known-host"), host, nil
}

// Enroll writes a confirmed pin once. A changed key at the same selected
// identity is a conflict; the caller must handle key rotation explicitly.
// This never edits the user's OpenSSH known_hosts file.
func (s *DirectTrustStore) Enroll(userConfig, systemConfig string, selection Selection, confirmed ConfirmedDirectHostKey) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, selection, confirmed.entry); err != nil {
		return err
	}
	path, host, err := s.fileFor(selection)
	if err != nil {
		return err
	}
	line := confirmed.entry.KnownHostsLine()
	if len(line) > 8<<10 || !strings.HasPrefix(string(line), host+" ssh-ed25519 ") {
		return ErrUnresolved
	}
	name := filepath.Base(path)
	unlock, err := lockPrivateTrustRecord(root, name+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		stored, readErr := s.read(root, name, host)
		if readErr != nil {
			return readErr
		}
		if string(stored) != string(line) {
			return ErrFingerprintMismatch
		}
		return RevalidateSelection(userConfig, systemConfig, selection)
	}
	if err != nil {
		return err
	}
	written, writeErr := file.Write(line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = root.Remove(name)
		return errors.Join(writeErr, closeErr)
	}
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}

// Rotate replaces an enrolled pin only after the caller supplies the exact
// currently approved fingerprint and a separately confirmed new key. The UI
// must gather both reviews explicitly; this library cannot prove a person
// supplied them. A missing or malformed old pin never creates new trust.
func (s *DirectTrustStore) Rotate(userConfig, systemConfig string, selection Selection, approvedPreviousSHA256 string, next ConfirmedDirectHostKey) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := validateDirectHostKeyEntry(userConfig, systemConfig, selection, next.entry); err != nil {
		return err
	}
	path, host, err := s.fileFor(selection)
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	unlock, err := lockPrivateTrustRecord(root, name+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return err
	}
	current, err := s.read(root, name, host)
	if err != nil {
		return err
	}
	currentFingerprint, err := pinnedFingerprint(current)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(approvedPreviousSHA256, "SHA256:") ||
		subtle.ConstantTimeCompare([]byte(approvedPreviousSHA256), []byte(currentFingerprint)) != 1 {
		return ErrFingerprintMismatch
	}
	line := next.entry.KnownHostsLine()
	if len(line) > 8<<10 || !strings.HasPrefix(string(line), host+" ssh-ed25519 ") || string(line) == string(current) {
		return ErrUnresolved
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := name + ".pending-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }()
	written, writeErr := file.Write(line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}

// PrepareEnrolledDirectProbe reads only an exact current app-owned pin and
// prepares the usual restricted, noninteractive diagnostic. It does not
// modify the pin or authorize a prompt or remote command.
func (s *DirectTrustStore) PrepareEnrolledDirectProbe(userConfig, systemConfig string, selection Selection) (*ProbePlan, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return nil, err
	}
	path, host, err := s.fileFor(selection)
	if err != nil {
		return nil, err
	}
	line, err := s.read(root, filepath.Base(path), host)
	if err != nil {
		return nil, err
	}
	plan, err := PrepareDirectProbe(userConfig, systemConfig, selection, line)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	expected := sha256.Sum256(line)
	plan.trustCheck = func() error {
		currentRoot, err := s.openRoot()
		if err != nil {
			return err
		}
		defer func() { _ = currentRoot.Close() }()
		current, err := s.read(currentRoot, name, host)
		if err != nil {
			return err
		}
		actual := sha256.Sum256(current)
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			return ErrChanged
		}
		return nil
	}
	plan.trustLock = func() (func(), error) {
		currentRoot, err := s.openRoot()
		if err != nil {
			return nil, err
		}
		unlock, err := lockPrivateTrustRecord(currentRoot, name+".lock")
		if err != nil {
			_ = currentRoot.Close()
			return nil, err
		}
		return func() {
			unlock()
			_ = currentRoot.Close()
		}, nil
	}
	if err := plan.Revalidate(); err != nil {
		_ = plan.Close()
		return nil, err
	}
	return plan, nil
}

// EnrolledDirectHostKeyFingerprint reports the SHA256 fingerprint of the
// exact current app-owned pin. It reads only local files; it cannot establish
// that the remote host is reachable or that its agent is installed.
func (s *DirectTrustStore) EnrolledDirectHostKeyFingerprint(userConfig, systemConfig string, selection Selection) (string, error) {
	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return "", err
	}
	path, host, err := s.fileFor(selection)
	if err != nil {
		return "", err
	}
	line, err := s.read(root, filepath.Base(path), host)
	if err != nil {
		return "", err
	}
	fingerprint, err := pinnedFingerprint(line)
	if err != nil {
		return "", err
	}
	if err := RevalidateSelection(userConfig, systemConfig, selection); err != nil {
		return "", err
	}
	return fingerprint, nil
}

func pinnedFingerprint(line []byte) (string, error) {
	fields := strings.Fields(string(line))
	if len(fields) != 3 {
		return "", ErrProbeUnsupported
	}
	blob, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil || !validED25519Blob(blob) {
		return "", ErrProbeUnsupported
	}
	fingerprint := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(fingerprint[:]), nil
}

func (s *DirectTrustStore) read(root *os.Root, name, host string) ([]byte, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, ErrTrustNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8<<10 {
		return nil, ErrProbeUnsupported
	}
	file, err := openPrivatePin(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || opened.Size() > 8<<10 || !os.SameFile(info, opened) {
		return nil, ErrProbeUnsupported
	}
	line, err := io.ReadAll(io.LimitReader(file, 8<<10+1))
	if err != nil || len(line) > 8<<10 {
		return nil, ErrProbeUnsupported
	}
	fields := strings.Fields(string(line))
	if len(fields) != 3 || fields[0] != host || fields[1] != "ssh-ed25519" || !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
		return nil, ErrProbeUnsupported
	}
	blob, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil || !validED25519Blob(blob) || string(line) != fmt.Sprintf("%s ssh-ed25519 %s\n", host, base64.StdEncoding.EncodeToString(blob)) {
		return nil, ErrProbeUnsupported
	}
	return line, nil
}
