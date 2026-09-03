#!/bin/sh

set -eu

readonly TERMLINKS_REPOSITORY="Ratul1997/termlinks"
readonly TERMLINKS_RELEASES="https://github.com/${TERMLINKS_REPOSITORY}/releases"

fail() {
  printf 'Termlinks installer: %s\n' "$1" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v awk >/dev/null 2>&1 || fail "awk is required"
command -v install >/dev/null 2>&1 || fail "install is required"

case "$(uname -s)" in
  Darwin) termlinks_os="darwin" ;;
  Linux) termlinks_os="linux" ;;
  *) fail "only macOS and Linux are currently supported" ;;
esac

case "$(uname -m)" in
  arm64|aarch64) termlinks_arch="arm64" ;;
  x86_64|amd64) termlinks_arch="amd64" ;;
  *) fail "unsupported CPU architecture: $(uname -m)" ;;
esac

termlinks_version="${TERMLINKS_VERSION:-}"
if [ -z "$termlinks_version" ]; then
  termlinks_latest_url=$(curl --fail --silent --show-error --location \
    --proto '=https' --tlsv1.2 --retry 3 --output /dev/null --write-out '%{url_effective}' \
    "${TERMLINKS_RELEASES}/latest") || fail "could not resolve the latest GitHub release"
  termlinks_version=${termlinks_latest_url##*/v}
fi
termlinks_version=${termlinks_version#v}
printf '%s' "$termlinks_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$' || \
  fail "invalid release version: $termlinks_version"

termlinks_archive="termlinks_${termlinks_version}_${termlinks_os}_${termlinks_arch}.tar.gz"
termlinks_download_base="${TERMLINKS_RELEASES}/download/v${termlinks_version}"
if [ -n "${TERMLINKS_INSTALL_DIR:-}" ]; then
  termlinks_install_dir=$TERMLINKS_INSTALL_DIR
else
  [ -n "${HOME:-}" ] || fail "HOME or TERMLINKS_INSTALL_DIR is required"
  termlinks_install_dir="${HOME}/.local/bin"
fi
case "$termlinks_install_dir" in
  /*) ;;
  *) fail "TERMLINKS_INSTALL_DIR must be an absolute path" ;;
esac
termlinks_temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/termlinks-install.XXXXXX") || fail "could not create a temporary directory"
termlinks_staged="${termlinks_install_dir}/.termlinks-install.$$"

cleanup() {
  rm -rf -- "$termlinks_temp_dir"
  rm -f -- "$termlinks_staged"
}
trap cleanup 0 HUP INT TERM

printf 'Downloading Termlinks %s for %s/%s...\n' "$termlinks_version" "$termlinks_os" "$termlinks_arch"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 \
  --output "${termlinks_temp_dir}/${termlinks_archive}" \
  "${termlinks_download_base}/${termlinks_archive}" || fail "could not download $termlinks_archive"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 \
  --output "${termlinks_temp_dir}/checksums.txt" \
  "${termlinks_download_base}/checksums.txt" || fail "could not download release checksums"

termlinks_expected=$(awk -v name="$termlinks_archive" '$2 == name { print $1 }' "${termlinks_temp_dir}/checksums.txt")
printf '%s' "$termlinks_expected" | grep -Eq '^[0-9a-fA-F]{64}$' || fail "the release checksum entry is missing or invalid"
if command -v sha256sum >/dev/null 2>&1; then
  termlinks_actual=$(sha256sum "${termlinks_temp_dir}/${termlinks_archive}" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  termlinks_actual=$(shasum -a 256 "${termlinks_temp_dir}/${termlinks_archive}" | awk '{ print $1 }')
else
  fail "sha256sum or shasum is required"
fi
[ "$termlinks_actual" = "$termlinks_expected" ] || fail "release checksum verification failed"

termlinks_entries=$(tar -tzf "${termlinks_temp_dir}/${termlinks_archive}") || fail "release archive is invalid"
[ "$termlinks_entries" = "termlinks" ] || fail "release archive contains unexpected paths"
tar -xzf "${termlinks_temp_dir}/${termlinks_archive}" -C "$termlinks_temp_dir" || fail "could not extract the release"
termlinks_candidate="${termlinks_temp_dir}/termlinks"
[ -f "$termlinks_candidate" ] && [ ! -L "$termlinks_candidate" ] || fail "release executable is missing or unsafe"
chmod 755 "$termlinks_candidate"

if [ "$termlinks_os" = "darwin" ]; then
  codesign --verify --strict "$termlinks_candidate" >/dev/null 2>&1 || fail "macOS code-signature verification failed"
fi
[ "$("$termlinks_candidate" version)" = "termlinks ${termlinks_version}" ] || fail "release executable reported the wrong version"

mkdir -p "$termlinks_install_dir" || fail "could not create $termlinks_install_dir"
[ -d "$termlinks_install_dir" ] || fail "install destination is not a directory"
install -m 755 "$termlinks_candidate" "$termlinks_staged" || fail "could not stage the executable"
mv -f "$termlinks_staged" "${termlinks_install_dir}/termlinks" || fail "could not install the executable"

printf 'Installed Termlinks %s to %s/termlinks\n' "$termlinks_version" "$termlinks_install_dir"
case ":${PATH}:" in
  *":${termlinks_install_dir}:"*) ;;
  *)
    printf '\nAdd Termlinks to your PATH, then restart your shell:\n'
    printf '  export PATH="%s:$PATH"\n' "$termlinks_install_dir"
    ;;
esac
printf '\nNext:\n'
printf '  termlinks token\n'
printf '  termlinks codex     # or: termlinks claude, termlinks bash\n'
