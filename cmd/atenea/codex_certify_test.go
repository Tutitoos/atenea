package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCertificationFlagsPermitDocumentedIDFirstForm(t *testing.T) {
	got := flagsBeforeID([]string{"certificate-id", "--markdown", "--state", "/tmp/state"}, map[string]bool{"--state": true})
	want := []string{"--markdown", "--state", "/tmp/state", "certificate-id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestCodexLoginStatusRejectsNegativeTextContainingLoggedIn(t *testing.T) {
	if isCodexLoggedInStatus("Not logged in") {
		t.Fatal("negative login status accepted")
	}
	if !isCodexLoggedInStatus("Logged in using ChatGPT") {
		t.Fatal("positive login status rejected")
	}
}

func TestDesktopHelperScannerAcceptsBoundedAccessibilityResponse(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 1<<20)
	scan := desktopHelperScanner(bytes.NewReader(append(payload, '\n')))
	if !scan.Scan() || len(scan.Bytes()) != len(payload) || scan.Err() != nil {
		t.Fatalf("large bounded helper response failed: %v", scan.Err())
	}
}

func TestCertificateTTLRejectsOverflowingDayCount(t *testing.T) {
	if _, err := parseCertificateTTL("999999999999999999d"); err == nil {
		t.Fatal("overflowing TTL accepted")
	}
	if got, err := parseCertificateTTL("30d"); err != nil || got != 30*24*time.Hour {
		t.Fatalf("30d=%s err=%v", got, err)
	}
}

func TestPinnedCertificationKeyCannotBeReplacedOrSymlinked(t *testing.T) {
	publicA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "certifier.pub")
	if err := writePinnedPublicKey(path, publicA); err != nil {
		t.Fatal(err)
	}
	if err := writePinnedPublicKey(path, publicA); err != nil {
		t.Fatal(err)
	}
	if err := writePinnedPublicKey(path, publicB); err == nil {
		t.Fatal("different key replaced pinned key")
	}
	symlink := filepath.Join(t.TempDir(), "certifier-link.pub")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if err := writePinnedPublicKey(symlink, publicA); err == nil {
		t.Fatal("symlinked public key accepted")
	}
}
