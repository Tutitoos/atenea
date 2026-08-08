package orchestrator

import (
	"slices"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Floor is the standing grant a dispatch composes its permission from: what
// the caller gets without asking, before anything the request itself asked
// for.
//
// The zero value is the settings file's own standing grant, which is what a
// command typed at a terminal runs on and what every dispatch used to run on.
// A chat opened by a connected client carries a floor of its own instead, so
// the operator widening their own line stops widening every client with it.
//
// This is a value rather than a plain []contract.Effect because an operator
// who writes "clients may do nothing but read" hands down an empty list, and a
// nil slice already means "nothing was said". Collapsing those two would grant
// the whole standing floor to precisely the client that was meant to have
// none, which is the one mistake a permission type must not make quietly.
type Floor struct {
	effects []contract.Effect
	set     bool
}

// FloorOf pins a floor to exactly these effects, the empty list included.
func FloorOf(effects []contract.Effect) Floor {
	return Floor{effects: slices.Clone(effects), set: true}
}

// Or answers with the pinned floor, or with the standing grant when nothing
// was pinned. The caller passes its own standing grant rather than the floor
// reaching for one, because a floor that could find the standing grant by
// itself would be a floor that could be forgotten and still look right.
func (f Floor) Or(standing []contract.Effect) []contract.Effect {
	if !f.set {
		return standing
	}
	return f.effects
}

// Allows reports whether an effect is inside the floor. A floor nobody pinned
// allows everything, because the question only means anything once somebody
// has drawn a line: the standing grant is not a ceiling, it is a gift.
func (f Floor) Allows(effect contract.Effect) bool {
	return !f.set || slices.Contains(f.effects, effect)
}

// Effects is what was pinned, for a screen to print. It is nil when nothing
// was, which reads the same on a screen as the standing grant it stands in for.
func (f Floor) Effects() []contract.Effect {
	return slices.Clone(f.effects)
}
