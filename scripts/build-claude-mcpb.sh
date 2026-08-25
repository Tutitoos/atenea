#!/usr/bin/env bash

set -euo pipefail

# Packs the Claude Desktop extension around a binary built from this checkout.
#
# It used to copy whatever sat at one developer's ~/.local/bin/atenea, which
# made the artifact a property of that machine rather than of this source tree:
# an installed binary can be any age, and nothing compared it against the
# version the manifest claims. Building here means the .mcpb contains the tree
# it was built from, and the check below means it cannot claim otherwise.
#
# ATENEA_BINARY still overrides, for packing a downloaded release artifact
# rather than a local build -- but the version check applies to it too.

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mcpb_path=${MCPB_BIN:-mcpb}
manifest_path="$repo_dir/packaging/claude-desktop/manifest.json"
stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/atenea-mcpb.XXXXXX")

cleanup() {
	rm -rf "$stage_dir"
}
trap cleanup EXIT

test -f "$manifest_path" || {
	echo "manifest not found: $manifest_path" >&2
	exit 1
}

command -v "$mcpb_path" >/dev/null 2>&1 || {
	echo "the mcpb packer is not installed; set MCPB_BIN or install it" >&2
	exit 1
}

manifest_version=$(
	awk -F'"' '/"version"[[:space:]]*:/ { print $4; exit }' "$manifest_path"
)
test -n "$manifest_version" || {
	echo "manifest declares no version: $manifest_path" >&2
	exit 1
}

if [ -n "${ATENEA_BINARY:-}" ]; then
	binary_path=$ATENEA_BINARY
	test -x "$binary_path" || {
		echo "ATENEA_BINARY is not executable: $binary_path" >&2
		exit 1
	}
else
	binary_path="$stage_dir/atenea"
	(cd "$repo_dir" && go build -trimpath -buildvcs=false -o "$binary_path" ./cmd/atenea)
fi

# The one check the old script had no way to make. A .mcpb whose manifest says
# one version and whose binary reports another is the failure that looks like a
# working install right up until somebody reports a bug against the wrong code.
binary_version=$("$binary_path" version | awk '/^atenea/ { print $2; exit }' | cut -d+ -f1)
test "$binary_version" = "$manifest_version" || {
	echo "manifest claims ${manifest_version}, the binary reports ${binary_version}" >&2
	exit 1
}

output_path=${MCPB_OUTPUT:-"$repo_dir/dist/atenea-${manifest_version}.mcpb"}
mkdir -p "$stage_dir/server" "$(dirname "$output_path")"
cp "$manifest_path" "$stage_dir/manifest.json"
install -m 755 "$binary_path" "$stage_dir/server/atenea"

"$mcpb_path" validate "$stage_dir"
"$mcpb_path" pack "$stage_dir" "$output_path"
printf 'MCPB: %s (atenea %s)\n' "$output_path" "$binary_version"
