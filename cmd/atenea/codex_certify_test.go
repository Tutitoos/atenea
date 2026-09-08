package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/codexcert"
)

func TestCertificationFlagsPermitDocumentedIDFirstForm(t *testing.T) {
	got := flagsBeforeID([]string{"certificate-id", "--markdown", "--state", "/tmp/state"}, map[string]bool{"--state": true})
	want := []string{"--markdown", "--state", "/tmp/state", "certificate-id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%q want=%q", got, want)
	}
	singleDash := flagsBeforeID([]string{"certificate-id", "-state", "/tmp/state"}, map[string]bool{"--state": true})
	wantSingleDash := []string{"-state", "/tmp/state", "certificate-id"}
	if !reflect.DeepEqual(singleDash, wantSingleDash) {
		t.Fatalf("single-dash args=%q want=%q", singleDash, wantSingleDash)
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
	payload := bytes.Repeat([]byte{'x'}, 8<<20)
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

func TestCertificationRebuildAcceptsAnInstalledExecutableDirectory(t *testing.T) {
	t.Chdir("../..")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const revision = "9b34dd0215c098a22a0ff7bd6e2be40b2aacac02"
	binary := filepath.Join(dir, "atenea")
	command := exec.Command("go", certificationBuildArgs(revision, binary)...)
	command.Env = append(os.Environ(), "GOFLAGS=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	if runtime.GOOS == "darwin" {
		sign := exec.Command("/usr/bin/codesign", "--force", "--options", "runtime", "--sign", "-", binary)
		if output, err := sign.CombinedOutput(); err != nil {
			t.Fatalf("sign fixture: %v: %s", err, output)
		}
	}
	if err := verifyCertificationRebuild(binary, revision); err != nil {
		t.Fatalf("verify from traversable install directory: %v", err)
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	revisionOffset := bytes.Index(contents, []byte(revision))
	if revisionOffset < 0 {
		t.Fatal("built fixture does not contain its stamped revision")
	}
	contents[revisionOffset] ^= 1
	if err := os.WriteFile(binary, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyCertificationRebuild(binary, revision); err == nil {
		t.Fatal("modified executable matched the reproducible rebuild")
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

func TestDesktopChallengeRequiresVisibleExactNoticeAndCompleteReceipt(t *testing.T) {
	c := codexcert.Challenge{Nonce: "nonce", InvocationID: "invocation"}
	prompt := desktopChallengePrompt("certificate", c, "/private/bin/atenea", "/private/state")
	if strings.Contains(prompt, "ATENEA · codex.certify.challenge") || !strings.Contains(prompt, "start the final answer with that same constructed Markdown line") || !strings.Contains(prompt, "Do not format the tool or invocation as inline code") {
		t.Fatalf("Desktop prompt does not preserve the exact visible notice: %s", prompt)
	}
	if strings.Contains(prompt, "'/private/state' .") || !strings.Contains(prompt, "'/private/state'\nImmediately before") {
		t.Fatalf("Desktop command is not isolated from prose punctuation: %s", prompt)
	}
	for _, field := range []string{"nonce=", "run", "workflow", "invocation", "result_proof", "desktop_process_receipt", "activity", "Progreso"} {
		if !strings.Contains(prompt, field) {
			t.Fatalf("Desktop prompt omitted %q", field)
		}
	}
}
