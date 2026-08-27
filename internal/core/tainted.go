package core

import (
	"slices"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A chat that has read this machine's screen may not then act on it, unless
// the operator has said otherwise in settings.
//
// # The switch, and what turning it on costs
//
// `[desktop] look_then_act` is false as shipped, and the default IS the
// control rather than a cautious guess about one. Turned on, everything below
// still happens -- the reading is still noticed, the order is still known --
// and the refusal simply does not fire. That is the whole of the change,
// because an operator driving their own desktop wants exactly the loop this
// otherwise forbids: look, click, look again.
//
// What it costs is stated here rather than in a release note, because this is
// where somebody reads it: with it on, a sentence inside somebody else's email
// can reach the pointer, and Atenea runs no classifier over what it captured
// to notice. What still stands is the deny-list, which outranks even the "*"
// allow-list token, the hard refusal to type into a secure field, credential
// redaction in desktop.type, and the receipts.
//
// # The hole this closes
//
// desktop.inspect and desktop.screenshot return what a window chose to
// display, which is written by whoever controls that window -- an email, a web
// page, a chat someone else is in. Marking the result untrusted, which those
// capabilities do, tells a reader where it came from. It does not stop anyone
// acting on it, and reading a coordinate and then clicking it is one call
// after another with nothing in between. That is the whole of a prompt
// injection reaching the pointer.
//
// So the rule is about ORDER within one chat rather than about content: once
// screen content has been handed over, this chat may no longer cause a device
// effect that changes something. Nothing has to judge whether a particular
// sentence was an instruction, which is fortunate, because nothing can.
//
// # What it costs, said plainly
//
// Look-then-act in one session is exactly what somebody automating a desktop
// wants, and this refuses it. That is the point rather than a regrettable side
// effect: look-then-act with no human in between is also precisely the shape
// of the attack. The way to act is `atenea desktop`, which shows what will
// happen and waits for a yes -- a human deciding, which is the thing the rule
// exists to require and the thing a marked-untrusted field cannot supply.
//
// # Why it is here and not in the adapter
//
// This is session state. internal/adapter/desktop is one object shared by
// every chat and is handed a RunRequest that carries no identity, so it cannot
// tell one caller's history from another's -- and a rule about what THIS chat
// did has to be enforced where chats live.
type taint struct {
	// read is set once this chat has been handed screen content. It never
	// clears: a chat that has seen a window cannot un-see it, and an
	// expiry would only mean an attacker waits.
	read bool
	// permitted mirrors `[desktop] look_then_act` for the life of this chat.
	//
	// The reading is still recorded when it is true, rather than skipped as an
	// optimization, so the two states differ only in whether the refusal
	// fires. A build that stopped noticing would make the switch's meaning
	// depend on when a chat happened to connect, which is the kind of rule
	// nobody can reason about at the moment they need to.
	permitted bool
}

// observing reports whether a capability hands back what is on the screen.
//
// Named against the capability rather than inferred from its effects, because
// `device` alone does not distinguish reading from acting and the two need
// opposite treatment here.
func observing(capability string) bool {
	return capability == "desktop.inspect" || capability == "desktop.screenshot"
}

// acting reports whether a capability changes something through the machine's
// own input devices.
//
// Read off the declared effects rather than a list of names, so a capability
// added later is covered without anybody remembering to add it here. That
// matters more than the tidiness: the failure mode of a list is silent.
func acting(capability contract.Capability) bool {
	if !slices.Contains(capability.Effects, contract.EffectDevice) {
		return false
	}
	return slices.Contains(capability.Effects, contract.EffectWrite) ||
		slices.Contains(capability.Effects, contract.EffectExternal)
}

// refuseIfTainted stops an act that follows a look, unless settings allow it.
func (t *taint) refuseIfTainted(capability contract.Capability) error {
	if t.permitted || !acting(capability) || !t.read {
		return nil
	}
	return contract.Fail(contract.FailurePermissionDenied,
		"%s changes something through this machine's own input, and this chat has already been "+
			"handed what is on the screen. What a window displays is written by whoever controls "+
			"it, so acting on it from here would let that text move the pointer. Use "+
			"`atenea desktop %s`, which shows what will happen and waits for a person to agree",
		capability.ID, strings.TrimPrefix(capability.ID, "desktop."))
}

// note records that screen content has been handed over.
func (t *taint) note(capability string) {
	if observing(capability) {
		t.read = true
	}
}
