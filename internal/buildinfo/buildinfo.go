// Package buildinfo carries the version stamped into the binary at link time.
package buildinfo

// Version is the Atenea product version.
//
// It follows three-number SemVer and is independent from the contract version
// in pkg/contract: the product is in alpha (0.x.y) and reaches 1.0.0 only when
// it goes stable, while the wire contract adapters compile against is already a
// commitment.
//
// Release builds override it with:
//
//	-ldflags "-X github.com/Tutitoos/atenea/internal/buildinfo.Version=0.1.0"
var Version = "0.1.0-dev"
