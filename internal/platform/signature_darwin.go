//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// SelfSignedStably reports whether this binary's code signature will keep a
// permission across a rebuild, and says why when it will not.
//
// It exists because the failure it catches is silent and the thing that would
// tell you is the thing that lies. macOS binds a TCC grant to the code's
// designated requirement. For a binary signed with any real certificate that
// requirement names an identifier and an anchor, and survives being rebuilt.
// For an unsigned or ad-hoc one -- which is what `go build` leaves behind --
// it is pinned to a cdhash that changes on every single build, measured even
// for a rebuild of identical source. Replace the installed binary without
// re-signing and Accessibility stops working, while System Settings goes on
// showing the permission as granted because the entry is still there.
//
// A settings hook cannot help here, which is worth writing down because it is
// the obvious idea: the thing that recompiles Atenea is `go build`, which has
// never read atenea.toml and does not know Atenea exists. There is no moment
// at which a configured hook could fire. Noticing afterwards is the only
// mechanism available, so this is it.
func SelfSignedStably() (bool, string) {
	stableOnce.Do(func() {
		stable, stableWhy = checkOwnSignature()
	})
	return stable, stableWhy
}

var (
	stableOnce sync.Once
	stable     bool
	stableWhy  string
)

func checkOwnSignature() (bool, string) {
	self, err := os.Executable()
	if err != nil {
		// Not knowing is not the same as knowing it is wrong, and reporting a
		// problem here would put a warning on every status screen of a
		// platform where this could not be checked.
		return true, ""
	}
	// The designated requirement rather than the signature's presence: an
	// ad-hoc signature IS a signature, and asking whether one exists would
	// answer yes for exactly the case this is about.
	out, err := exec.Command("codesign", "-d", "-r-", self).CombinedOutput()
	if err != nil {
		return true, ""
	}
	text := string(out)
	if !strings.Contains(text, "designated =>") {
		return true, ""
	}
	if strings.Contains(text, "cdhash") {
		return false, "this binary is ad-hoc signed, so a granted Accessibility or Screen " +
			"Recording permission is pinned to its exact contents and will stop working the " +
			"next time it is rebuilt -- while System Settings goes on showing it as granted. " +
			"Sign it with any certificate: scripts/install-dev.sh does it, or `codesign -f -s " +
			"IDENTITY --identifier com.tutitoos.atenea --options runtime` on the installed copy"
	}
	return true, ""
}
