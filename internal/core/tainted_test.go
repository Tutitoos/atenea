package core

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func capabilityWith(id string, effects ...contract.Effect) contract.Capability {
	return contract.Capability{ID: id, Effects: effects}
}

// The hole, stated as a test. Reading a coordinate off a window and then
// clicking it is one call after another with nothing in between, and that is
// the whole of a prompt injection reaching the pointer.
func TestActingAfterLookingIsRefused(t *testing.T) {
	var chat taint
	inspect := capabilityWith("desktop.inspect", contract.EffectRead, contract.EffectDevice)
	click := capabilityWith("desktop.click", contract.EffectRead, contract.EffectDevice,
		contract.EffectWrite, contract.EffectExternal)

	if err := chat.refuseIfTainted(click); err != nil {
		t.Fatalf("acting before looking was refused: %v", err)
	}
	chat.note(inspect.ID)
	err := chat.refuseIfTainted(click)
	if err == nil {
		t.Fatal("a chat clicked on what it had just read off the screen")
	}
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("failure = %v, want permission_denied", got)
	}
	// The remedy has to be in the message. A refusal with no way forward is a
	// refusal somebody routes around.
	if !strings.Contains(err.Error(), "atenea desktop click") {
		t.Errorf("refusal = %q, want it to name the confirmed path", err)
	}
}

// A capture taints exactly as a tree walk does: pixels are as much somebody
// else's writing as text is.
func TestAScreenshotTaintsToo(t *testing.T) {
	var chat taint
	chat.note("desktop.screenshot")
	if err := chat.refuseIfTainted(capabilityWith("desktop.type",
		contract.EffectDevice, contract.EffectWrite)); err == nil {
		t.Error("typing was allowed after a screenshot")
	}
}

// And looking again is always fine. The rule is about acting on what was seen,
// not about rationing how much a chat may see.
func TestLookingTwiceIsFine(t *testing.T) {
	var chat taint
	chat.note("desktop.inspect")
	for _, id := range []string{"desktop.inspect", "desktop.screenshot", "desktop.apps"} {
		if err := chat.refuseIfTainted(capabilityWith(id,
			contract.EffectRead, contract.EffectDevice)); err != nil {
			t.Errorf("%s was refused after looking: %v", id, err)
		}
	}
}

// Nothing outside the desktop is touched by this. A chat that read a window
// must still be able to search code, or the rule would quietly become "having
// looked at the screen ends the conversation".
func TestOrdinaryWorkIsUnaffected(t *testing.T) {
	var chat taint
	chat.note("desktop.inspect")
	for _, capability := range []contract.Capability{
		capabilityWith("code.search", contract.EffectRead, contract.EffectProcess),
		capabilityWith("repository.index", contract.EffectWrite, contract.EffectProcess),
	} {
		if err := chat.refuseIfTainted(capability); err != nil {
			t.Errorf("%s was refused: %v", capability.ID, err)
		}
	}
}

// Read off the effects rather than a list of names, so a capability added
// later is covered without anybody remembering to come back here. The failure
// mode of a list is that nothing says it went stale.
func TestACapabilityNobodyListedIsStillCovered(t *testing.T) {
	var chat taint
	chat.note("desktop.inspect")
	invented := capabilityWith("desktop.paste_from_somewhere",
		contract.EffectRead, contract.EffectDevice, contract.EffectWrite)
	if err := chat.refuseIfTainted(invented); err == nil {
		t.Error("a device capability nobody listed here acted after a look")
	}
}

// Moving the pointer changes nothing and is not an act. Refusing it would make
// the rule read as suspicion of the whole surface rather than a statement about
// what can be changed.
func TestMovingThePointerIsNotActing(t *testing.T) {
	var chat taint
	chat.note("desktop.inspect")
	if err := chat.refuseIfTainted(capabilityWith("desktop.move",
		contract.EffectRead, contract.EffectDevice)); err != nil {
		t.Errorf("move was refused: %v", err)
	}
}
