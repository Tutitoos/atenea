//go:build windows

package sshinventory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateTrustStoreRejectsReplacedDirectory(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "app-trust")
	store, err := OpenPrivateDirectTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := createPrivateTrustDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := store.checkRoot(); !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("store accepted a different private directory: %v", err)
	}
}

func allowEveryoneWindows(t *testing.T, path string) {
	t.Helper()
	user, err := currentWindowsUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)(A;;FA;;;" + user.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivateTrustStoreRejectsOpenACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app-trust")
	store, err := OpenPrivateDirectTrustStore(root)
	if err != nil {
		t.Fatal(err)
	}
	allowEveryoneWindows(t, root)
	if err := store.checkRoot(); !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("store with Everyone access remained usable: %v", err)
	}
}

func TestWindowsPrivateTrustStoreRejectsOpenPinACL(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	writeFixture(t, config, "Host selected\n HostName host.example.test\n User person\n")
	selection, err := ResolveStatic(config, "", "selected")
	if err != nil {
		t.Fatal(err)
	}
	key, digest := ed25519FixtureKey()
	entry, err := MatchDirectED25519HostKey(config, "", selection, key, digest)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmDirectED25519HostKey(config, "", selection, entry, digest)
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
	allowEveryoneWindows(t, path)
	if got, err := store.EnrolledDirectHostKeyFingerprint(config, "", selection); got != "" || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("pin with Everyone access was read: %q, %v", got, err)
	}
	if plan, err := store.PrepareEnrolledDirectProbe(config, "", selection); plan != nil || !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("pin with Everyone access prepared a probe: %v", err)
	}
}
