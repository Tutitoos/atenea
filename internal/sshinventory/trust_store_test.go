package sshinventory

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateDirectTrustStoreEnrollmentAndInvalidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows ACL validation is required before enrollment")
	}
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n HostName host.example.test\n User person\n Port 2222\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	candidate, fingerprint := ed25519FixtureKey()
	confirmedKey := func(key, digest string) ConfirmedDirectHostKey {
		t.Helper()
		entry, err := MatchDirectED25519HostKey(config, "", selection, key, digest)
		if err != nil {
			t.Fatal(err)
		}
		confirmed, err := ConfirmDirectED25519HostKey(config, "", selection, entry, digest)
		if err != nil {
			t.Fatal(err)
		}
		return confirmed
	}
	confirmed := confirmedKey(candidate, fingerprint)
	store, err := OpenPrivateDirectTrustStore(filepath.Join(root, "app-trust"))
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := store.PrepareEnrolledDirectProbe(config, "", selection); plan != nil || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("unenrolled target accepted: %v", err)
	}
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); got != "" || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("unenrolled fingerprint = %q, %v", got, err)
	}
	if err := store.Enroll(config, "", selection, confirmed); err != nil {
		t.Fatal(err)
	}
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); err != nil || got != fingerprint {
		t.Fatalf("enrolled fingerprint = %q, %v", got, err)
	}
	if err := store.Enroll(config, "", selection, confirmed); err != nil {
		t.Fatalf("same pin did not enroll idempotently: %v", err)
	}
	plan, err := store.PrepareEnrolledDirectProbe(config, "", selection)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(plan.root, "known_hosts"))
	if err != nil || string(stored) != string(confirmed.entry.KnownHostsLine()) {
		t.Fatalf("enrolled probe pin differs: %v", err)
	}
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	path, _, err := store.fileFor(selection)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("persisted pin permissions: %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("malformed pin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if plan, err := store.PrepareEnrolledDirectProbe(config, "", selection); plan != nil || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("malformed stored pin accepted: %v", err)
	}
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); got != "" || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("malformed stored fingerprint = %q, %v", got, err)
	}
	if err := os.WriteFile(path, confirmed.entry.KnownHostsLine(), 0o600); err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(candidate)
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 1
	changedKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	changedDigest := sha256.Sum256(blob)
	changed := confirmedKey(changedKey, "SHA256:"+base64.RawStdEncoding.EncodeToString(changedDigest[:]))
	if err := store.Enroll(config, "", selection, changed); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("changed host key replaced an enrolled pin: %v", err)
	}
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n User person\n Port 2222\n")
	if plan, err := store.PrepareEnrolledDirectProbe(config, "", selection); plan != nil || !errors.Is(err, ErrChanged) {
		t.Fatalf("old config retained trust: %v", err)
	}
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); got != "" || !errors.Is(err, ErrChanged) {
		t.Fatalf("old config retained fingerprint: %q, %v", got, err)
	}
	current, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := store.PrepareEnrolledDirectProbe(config, "", current); plan != nil || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("changed config inherited trust: %v", err)
	}
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", current); got != "" || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("changed config inherited fingerprint: %q, %v", got, err)
	}
}

func TestPrivateDirectTrustStoreRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows ACL validation is required before enrollment")
	}
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenPrivateDirectTrustStore(unsafe); store != nil || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("group/world-accessible store accepted: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(unsafe, link); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenPrivateDirectTrustStore(link); store != nil || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("symlinked store accepted: %v", err)
	}
}

func TestPrivateDirectTrustStoreRejectsUnsafePin(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("private pin storage is implemented on macOS and Linux")
	}
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n HostName host.example.test\n User person\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	candidate, fingerprint := ed25519FixtureKey()
	entry, err := MatchDirectED25519HostKey(config, "", selection, candidate, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmDirectED25519HostKey(config, "", selection, entry, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenPrivateDirectTrustStore(filepath.Join(root, "app-trust"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enroll(config, "", selection, confirmed); err != nil {
		t.Fatal(err)
	}
	path, _, err := store.fileFor(selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		make func(t *testing.T)
	}{
		{"symlink", func(t *testing.T) {
			target := filepath.Join(root, "valid-pin")
			if err := os.WriteFile(target, confirmed.entry.KnownHostsLine(), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"group-readable", func(t *testing.T) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, confirmed.entry.KnownHostsLine(), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.make(t)
			if tt.name == "symlink" {
				file, err := openPrivatePin(path)
				if file != nil {
					_ = file.Close()
				}
				if !errors.Is(err, ErrProbeUnsupported) {
					t.Fatalf("no-follow open accepted a symlink: %v", err)
				}
			}
			if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); got != "" || !errors.Is(err, ErrProbeUnsupported) {
				t.Fatalf("unsafe pin fingerprint = %q, %v", got, err)
			}
			if plan, err := store.PrepareEnrolledDirectProbe(config, "", selection); plan != nil || !errors.Is(err, ErrProbeUnsupported) {
				t.Fatalf("unsafe pin prepared a probe: %v", err)
			}
		})
	}
}
