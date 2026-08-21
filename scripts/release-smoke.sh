#!/usr/bin/env bash
set -euo pipefail

version="${1:-${ATENEA_VERSION:-}}"
repository="${ATENEA_REPOSITORY:-Tutitoos/atenea}"

[ -n "$version" ] || {
  echo "usage: release-smoke.sh VERSION" >&2
  exit 2
}
version="${version#v}"

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }

root="$(mktemp -d "${TMPDIR:-/tmp}/atenea-release-smoke.XXXXXX")"
trap 'rm -rf "$root"' EXIT
installer="$root/atenea-install.sh"
install_dir="$root/bin"

curl --fail --silent --show-error --location \
  "https://github.com/${repository}/releases/download/v${version}/atenea-install.sh" \
  -o "$installer"
chmod 0755 "$installer"

ATENEA_REPOSITORY="$repository" ATENEA_INSTALL_DIR="$install_dir" \
  bash "$installer" --version "$version"
actual="$($install_dir/atenea version | awk '/^atenea/ {print $2}')"
[ "$actual" = "$version" ] || {
  echo "installed version $actual, expected $version" >&2
  exit 1
}

# Reinstalling the same pinned release exercises the update path without
# requiring a second network version. The installer must preserve one copy.
ATENEA_REPOSITORY="$repository" ATENEA_INSTALL_DIR="$install_dir" \
  bash "$installer" --version "$version"
test -f "$install_dir/atenea.previous"

ATENEA_INSTALL_DIR="$install_dir" bash "$installer" --rollback
test -x "$install_dir/atenea"

ATENEA_INSTALL_DIR="$install_dir" bash "$installer" --uninstall
test ! -e "$install_dir/atenea"
test ! -e "$install_dir/atenea.previous"

echo "release smoke passed: ${repository} v${version}"
