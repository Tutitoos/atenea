#!/usr/bin/env bash
set -euo pipefail

repository="${ATENEA_REPOSITORY:-Tutitoos/atenea}"
version="${ATENEA_VERSION:-}"
install_dir="${ATENEA_INSTALL_DIR:-$HOME/.local/bin}"
install_service=0
action="install"

usage() {
  cat <<'EOF'
Usage: install.sh --version VERSION [--service]
       install.sh --rollback
       install.sh --uninstall [--service]

Environment:
  ATENEA_REPOSITORY  GitHub repository, default Tutitoos/atenea
  ATENEA_VERSION     release version, for example 0.10.4
  ATENEA_INSTALL_DIR destination, default ~/.local/bin

Update behavior:
  Installing a new version keeps the previous binary at atenea.previous.
  --rollback restores that binary; --uninstall removes the binary and, when
  --service is given, the installed background service.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || { echo "--version needs a value" >&2; exit 2; }
      version="$2"
      shift 2
      ;;
    --service)
      install_service=1
      shift
      ;;
    --rollback)
      [ "$action" = "install" ] || { echo "choose only one action" >&2; exit 2; }
      action="rollback"
      shift
      ;;
    --uninstall)
      [ "$action" = "install" ] || { echo "choose only one action" >&2; exit 2; }
      action="uninstall"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

binary="$install_dir/atenea"
previous="$install_dir/atenea.previous"

if [ "$action" = "uninstall" ]; then
  if [ "$install_service" -eq 1 ] && [ -x "$binary" ]; then
    "$binary" service uninstall || {
      echo "could not remove the background service; binary was kept" >&2
      exit 1
    }
  fi
  removed=0
  for path in "$binary" "$previous"; do
    if [ -e "$path" ]; then
      rm -f "$path"
      removed=1
    fi
  done
  if [ "$removed" -eq 1 ]; then
    echo "uninstalled $install_dir/atenea"
  else
    echo "nothing installed at $install_dir/atenea"
  fi
  exit 0
fi

if [ "$action" = "rollback" ]; then
  [ -f "$previous" ] || { echo "no rollback binary at $previous" >&2; exit 1; }
  rollback_tmp="$install_dir/.atenea.rollback.$$"
  trap 'rm -f "$rollback_tmp"' EXIT
  if [ -f "$binary" ]; then
    install -m 0755 "$binary" "$rollback_tmp"
  fi
  install -m 0755 "$previous" "$binary"
  if [ -f "$rollback_tmp" ]; then
    mv "$rollback_tmp" "$previous"
  else
    rm -f "$previous"
  fi
  echo "rolled back $binary"
  exit 0
fi

[ -n "$version" ] || { echo "a release version is required" >&2; usage >&2; exit 2; }
version="${version#v}"

case "$(uname -s)" in
  Linux) artifact_os="linux" ;;
  Darwin) artifact_os="darwin" ;;
  *) echo "the published installer supports Linux and macOS; build from source on $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) artifact_arch="amd64" ;;
  arm64|aarch64) artifact_arch="arm64" ;;
  *) echo "no published artifact exists for $(uname -s)/$(uname -m); build from source" >&2; exit 1 ;;
esac

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool="shasum"
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

artifact="atenea-${version}-${artifact_os}-${artifact_arch}"
base="https://github.com/${repository}/releases/download/v${version}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/atenea-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

curl --fail --silent --show-error --location "$base/$artifact" -o "$tmp/$artifact"
curl --fail --silent --show-error --location "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
checksum="$(awk -v artifact="$artifact" '$2 == artifact { print $1; exit }' "$tmp/SHA256SUMS")"
[ -n "$checksum" ] || { echo "SHA256SUMS has no entry for $artifact" >&2; exit 1; }
if [ "$checksum_tool" = "sha256sum" ]; then
  printf '%s  %s\n' "$checksum" "$tmp/$artifact" | sha256sum -c -
else
  printf '%s  %s\n' "$checksum" "$tmp/$artifact" | shasum -a 256 -c -
fi

mkdir -p "$install_dir"
if [ -f "$binary" ]; then
  install -m 0755 "$binary" "$previous"
fi
install -m 0755 "$tmp/$artifact" "$binary"
echo "installed $binary"

if [ "$install_service" -eq 1 ]; then
  "$install_dir/atenea" service install
  case "$artifact_os" in
    linux) echo "service installed; start it with: systemctl --user start atenea.service" ;;
    darwin) echo "service installed; start it with: launchctl kickstart -k gui/$(id -u)/com.tutitoos.atenea" ;;
  esac
fi
