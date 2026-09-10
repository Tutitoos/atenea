// Package protocolv1 exposes the canonical atenea.remote.v1 schema assets.
package protocolv1

import "embed"

// FS contains the canonical protocol schemas. Consumers should read these
// assets through this embedded filesystem rather than from the process cwd.
//
// Go embed omits empty directories and files or directories whose names begin
// with . or _. Those omitted entries contain no corpus bytes; this bundle does
// not and cannot detect empty source directories.
//
//go:embed *.json fixtures
var FS embed.FS
