package sshinventory

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnrolledSingleJumpProbeRequiresAndLocksBothPins(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("single-hop execution requires Unix process-group containment")
	}
	config, target, jump, pins := jumpFixture(t)
	store, err := OpenPrivateDirectTrustStore(filepath.Join(t.TempDir(), "trust"))
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(selected Selection, line string) (ConfirmedDirectHostKey, string) {
		t.Helper()
		fields := strings.Fields(line)
		blob, err := base64.StdEncoding.DecodeString(fields[2])
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(blob)
		fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
		entry, err := MatchSingleJumpED25519HostKey(config, "", target, jump, selected, fields[1]+" "+fields[2], fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		confirmed, err := ConfirmDirectED25519HostKey(config, "", selected, entry, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return confirmed, fingerprint
	}
	lines := strings.Split(strings.TrimSpace(string(pins)), "\n")
	targetKey, targetFingerprint := confirm(target, lines[0])
	jumpKey, jumpFingerprint := confirm(jump, lines[1])
	if plan, err := store.PrepareEnrolledSingleJumpProbe(config, "", target, jump); plan != nil || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("unenrolled route accepted: %v", err)
	}
	if err := store.EnrollSingleJumpPin(config, "", target, jump, target, targetKey); err != nil {
		t.Fatal(err)
	}
	if plan, err := store.PrepareEnrolledSingleJumpProbe(config, "", target, jump); plan != nil || !errors.Is(err, ErrTrustNotEnrolled) {
		t.Fatalf("half-enrolled route accepted: %v", err)
	}
	if err := store.EnrollSingleJumpPin(config, "", target, jump, jump, targetKey); !errors.Is(err, ErrChanged) {
		t.Fatalf("destination approval accepted for gateway: %v", err)
	}
	if err := store.EnrollSingleJumpPin(config, "", target, jump, jump, jumpKey); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PrepareEnrolledSingleJumpProbe(config, "", target, jump)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()
	if len(plan.Arguments()) != 0 {
		t.Fatal("enrolled jump plan exposed arguments that bypass pin revalidation")
	}
	unlock, err := plan.trustLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rotate(config, "", jump, jumpFingerprint, jumpKey); !errors.Is(err, ErrTrustBusy) {
		t.Fatalf("gateway rotation bypassed probe lock: %v", err)
	}
	if err := store.Rotate(config, "", target, targetFingerprint, targetKey); !errors.Is(err, ErrTrustBusy) {
		t.Fatalf("destination rotation bypassed probe lock: %v", err)
	}
	unlock()
	if err := plan.Revalidate(); err != nil {
		t.Fatal(err)
	}
	jumpToken, err := directKnownHostToken(jump)
	if err != nil {
		t.Fatal(err)
	}
	nextJumpKey, _ := confirm(jump, string(syntheticJumpPin(jumpToken, 'c')))
	if err := store.Rotate(config, "", jump, jumpFingerprint, nextJumpKey); err != nil {
		t.Fatal(err)
	}
	if err := plan.Revalidate(); !errors.Is(err, ErrChanged) {
		t.Fatalf("rotated gateway key retained earlier plan: %v", err)
	}
	freshPlan, err := store.PrepareEnrolledSingleJumpProbe(config, "", target, jump)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = freshPlan.Close() }()
	writeFixture(t, config, "Host selected\n HostName changed.example.test\n ProxyJump jump\nHost jump\n HostName jump.example.test\n")
	if err := freshPlan.Revalidate(); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed route retained enrolled approval: %v", err)
	}
}
