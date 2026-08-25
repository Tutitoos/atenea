package desktop

import (
	"os"
	"testing"
)

// The two signals are checked separately because each is weak alone and the
// combination is the whole point: launchd sets XPC_SERVICE_NAME for jobs it
// manages, but a shell can export anything, and a parent of pid 1 belongs to a
// launchd job and equally to anything reparented after its parent died.
func TestLaunchdIsRecognizedFromEitherSignal(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "com.tutitoos.atenea")
	if !UnderLaunchd() {
		t.Error("a launchd-managed job was not recognized")
	}
}

// "0" is what launchd puts there for a process it did NOT manage, so reading
// it as a yes would report every terminal as a service.
func TestTheNotManagedSentinelIsNotAService(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "0")
	if os.Getppid() == 1 {
		t.Skip("this process really is reparented; the ppid signal would answer instead")
	}
	if UnderLaunchd() {
		t.Error(`XPC_SERVICE_NAME="0" was read as a managed job`)
	}
}

func TestAnEmptyValueIsNotAService(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "")
	if os.Getppid() == 1 {
		t.Skip("this process really is reparented; the ppid signal would answer instead")
	}
	if UnderLaunchd() {
		t.Error("an empty XPC_SERVICE_NAME was read as a managed job")
	}
}
