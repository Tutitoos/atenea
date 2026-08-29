#!/usr/bin/env bash
# Build, sign and install Atenea and its desktop helper over the running copy.
#
# The signing is not polish. macOS binds a TCC grant to the code's designated
# requirement, and an unsigned binary's requirement is pinned to a hash that
# changes on every build -- even a rebuild of identical source, because neither
# `go build` nor `swiftc` is reproducible. Replace the installed binary without
# re-signing and Accessibility silently stops working while System Settings goes
# on showing it as granted, which is the worst kind of broken: the thing that
# tells you the state is the thing that is wrong.
#
# So this exists to make forgetting impossible rather than to save typing.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="${ATENEA_BIN:-$HOME/.local/bin/atenea}"
helper="${ATENEA_HELPER:-$HOME/.local/libexec/atenea-desktop-helper}"

# Any identity works: what matters is that the requirement stops being
# hash-pinned, not which certificate did it. An Apple Development certificate is
# free with any Apple ID and is enough for a machine you are sitting at.
identity="${ATENEA_SIGNING_IDENTITY:-}"
if [ -z "$identity" ]; then
	identity="$(security find-identity -v -p codesigning 2>/dev/null |
		awk '/Apple Development|Developer ID Application/ { print $2; exit }')"
fi
if [ -z "$identity" ]; then
	echo "No signing identity found." >&2
	echo "Atenea will install, but its Accessibility grant will die on the next build." >&2
	echo "Set ATENEA_SIGNING_IDENTITY, or create a free Apple Development certificate in Xcode." >&2
fi

sign() {
	[ -n "$identity" ] || return 0
	codesign --force --options runtime --identifier "$2" --sign "$identity" "$1"
	echo "  signed $(basename "$1") as $2"
}

backup_file() {
	local path="$1"
	if [ ! -f "$path" ]; then
		return 0
	fi
	local backup="${path}.atenea-backup.$(date +%Y%m%d%H%M%S)"
	cp -p "$path" "$backup"
	echo "  backup $(basename "$path") -> $(basename "$backup")" >&2
	echo "$backup"
}

previous_bin=""
previous_helper=""
rollback() {
	local status=$?
	if [ "$status" -eq 0 ]; then
		return
	fi
	echo "installation failed; restoring the previous Atenea binaries" >&2
	if [ -n "$previous_bin" ]; then
		cp -p "$previous_bin" "$bin" || true
	fi
	if [ -n "$previous_helper" ]; then
		cp -p "$previous_helper" "$helper" || true
	fi
	if [ "$(uname -s)" = "Darwin" ] && [ -x "$bin" ]; then
		"$bin" service install >/dev/null 2>&1 || true
	fi
	exit "$status"
}
trap rollback EXIT

echo "building"
cd "$root"
go build -trimpath -o /tmp/atenea-install ./cmd/atenea
if [ "$(uname -s)" = "Darwin" ]; then
	swift build -c release --package-path helper >/dev/null
fi

# Stopped before it is replaced. Overwriting a running binary is how you get a
# service whose process no longer matches the file it was started from.
if [ "$(uname -s)" = "Darwin" ]; then
	launchctl bootout "gui/$(id -u)/com.tutitoos.atenea" 2>/dev/null || true
	sleep 1
fi

echo "installing"
mkdir -p "$(dirname "$bin")" "$(dirname "$helper")"
previous_bin="$(backup_file "$bin")"
if [ "$(uname -s)" = "Darwin" ]; then
	previous_helper="$(backup_file "$helper")"
fi
cp /tmp/atenea-install "$bin"
sign "$bin" com.tutitoos.atenea
if [ "$(uname -s)" = "Darwin" ]; then
	cp "$root/helper/.build/release/atenea-desktop-helper" "$helper"
	sign "$helper" com.tutitoos.atenea.desktop
fi

if [ "$(uname -s)" = "Darwin" ]; then
	echo "restarting the service"
	"$bin" service install >/dev/null
	sleep 2
fi
"$bin" version
