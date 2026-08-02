// Package contract defines the wire contract shared by the Atenea core and its
// client adapters (omp, Claude Code, OpenCode).
//
// Adapters are dumb translators: they compile against this package and nothing
// else. No internal package may leak into it, and nothing here may know how any
// concrete CLI or tool behaves.
package contract

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a three-number SemVer.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
}

// Current is the version of this contract.
//
// It is versioned independently from the Atenea product version: the product is
// in alpha (0.x.y) while the format adapters compile against is already a
// commitment. Major bumps break adapters; minor bumps add fields without
// breaking them; patch is cosmetic.
var Current = Version{Major: 1, Minor: 1, Patch: 0}

// ParseVersion reads a MAJOR.MINOR.PATCH string.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", s)
	}
	var v Version
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(parts[i], 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: field %d is not a number", s, i+1)
		}
		*dst = n
	}
	return v, nil
}

func (v Version) String() string {
	return strconv.FormatUint(v.Major, 10) + "." +
		strconv.FormatUint(v.Minor, 10) + "." +
		strconv.FormatUint(v.Patch, 10)
}

// Supports reports whether a peer speaking version other can talk to a core
// speaking v.
//
// A peer that lags behind by minor versions is accepted on purpose: an adapter
// must not die the moment the core gains a field it does not know about. A peer
// ahead of the core is refused, because the core cannot honor a field it has
// never heard of.
func (v Version) Supports(other Version) bool {
	return v.Major == other.Major && other.Minor <= v.Minor
}
