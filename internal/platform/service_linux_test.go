//go:build linux

package platform

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestLinuxServiceHelpersAreDeterministic(t *testing.T) {
	props := properties("LoadState=loaded\nDescription= Atenea\nmalformed\n")
	if props["LoadState"] != "loaded" || props["Description"] != "Atenea" {
		t.Fatalf("properties = %#v", props)
	}
	if got := complaint(" first complaint \noperator advice", errors.New("exit status 1")); got != "first complaint" {
		t.Fatalf("complaint = %q, want first complaint", got)
	}
	if got := complaint("  ", errors.New("exit status 1")); got != "exit status 1" {
		t.Fatalf("empty complaint = %q, want exit status 1", got)
	}

	service, err := NewService("/opt/atenea", 1500*1000*1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(service.UnitText(), "TimeoutStopSec=7") {
		t.Fatalf("unit text did not round the stop timeout up: %s", service.UnitText())
	}
	if got := unitPath("atenea"); !strings.HasSuffix(got, filepath.Join("systemd", "user", "atenea.service")) {
		t.Fatalf("unit path = %q", got)
	}
	if got := LingerCommand(); !strings.HasPrefix(got, "loginctl enable-linger ") {
		t.Fatalf("LingerCommand = %q", got)
	}
}

func TestLinuxManagerSeparatesSuccessRefusalAndUnavailable(t *testing.T) {
	out, refused, err := manager("sh", "-c", "printf success")
	if err != nil || out != "success" || refused != "" {
		t.Fatalf("success = %q, %q, %v", out, refused, err)
	}

	out, refused, err = manager("sh", "-c", "printf 'first\\nsecond\\n' >&2; exit 1")
	if err != nil || out != "" || refused != "first" {
		t.Fatalf("refusal = %q, %q, %v", out, refused, err)
	}

	_, _, err = manager("atenea-test-command-that-does-not-exist")
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("missing manager error kind = %v, want unavailable", contract.KindOf(err))
	}
}

func TestLinuxWriteUnitAndDiskFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "systemd", "user", "atenea.service")
	if err := writeUnit(path, "[Service]\n"); err != nil {
		t.Fatalf("writeUnit: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "[Service]\n" {
		t.Fatalf("unit = %q, err = %v", got, err)
	}

	if got := contract.KindOf(diskFailure(path, fs.ErrPermission)); got != contract.FailurePermissionDenied {
		t.Fatalf("permission failure kind = %v", got)
	}
	if got := contract.KindOf(diskFailure(path, errors.New("disk missing"))); got != contract.FailureUnavailable {
		t.Fatalf("generic failure kind = %v", got)
	}

	parentFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := contract.KindOf(writeUnit(filepath.Join(parentFile, "unit"), "text")); got != contract.FailureUnavailable {
		t.Fatalf("parent failure kind = %v", got)
	}
	existingDir := filepath.Join(root, "existing-directory")
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := contract.KindOf(writeUnit(existingDir, "text")); got != contract.FailureUnavailable {
		t.Fatalf("rename failure kind = %v", got)
	}
}
