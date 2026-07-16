#!/bin/sh
set -eu

# Install or update poorman from the latest GitHub release.
# Usage: curl -fsSL https://raw.githubusercontent.com/ChokunPlayZ/poormanwebctrl/main/install.sh | sh

repo=${POORMAN_REPOSITORY:-ChokunPlayZ/poormanwebctrl}
install_dir=${POORMAN_INSTALL_DIR:-${XDG_BIN_HOME:-$HOME/.local/bin}}
requested_version=${POORMAN_VERSION:-latest}

for command in curl tar; do
	if ! command -v "$command" >/dev/null 2>&1; then
		echo "poorman installer: '$command' is required" >&2
		exit 1
	fi
done

case "$(uname -s):$(uname -m)" in
	Linux:x86_64|Linux:amd64) os=linux; arch=amd64 ;;
	Linux:aarch64|Linux:arm64) os=linux; arch=arm64 ;;
	Darwin:x86_64|Darwin:amd64) os=darwin; arch=amd64 ;;
	Darwin:arm64) os=darwin; arch=arm64 ;;
	*) echo "poorman installer: unsupported platform $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

if [ "$requested_version" = latest ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | awk -F'"' '/"tag_name"[[:space:]]*:/ {print $4; exit}')
	[ -n "$version" ] || { echo "poorman installer: could not determine the latest release" >&2; exit 1; }
else
	version=$requested_version
	case "$version" in v*) ;; *) version="v$version" ;; esac
fi

release=${version#v}
archive="poorman_${release}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t poorman)
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

curl -fsSL "$base/$archive" -o "$tmp_dir/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp_dir/checksums.txt"

expected=$(awk -v file="$archive" '$2 == file || $2 == "*" file {print $1; exit}' "$tmp_dir/checksums.txt")
[ -n "$expected" ] || { echo "poorman installer: checksum missing for $archive" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp_dir/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp_dir/$archive" | awk '{print $1}')
else
	echo "poorman installer: sha256sum or shasum is required" >&2
	exit 1
fi
[ "$expected" = "$actual" ] || { echo "poorman installer: checksum verification failed" >&2; exit 1; }

mkdir -p "$install_dir"
tar -xzf "$tmp_dir/$archive" -C "$tmp_dir"
install -m 0755 "$tmp_dir/poorman" "$install_dir/poorman"

echo "poorman $version installed at $install_dir/poorman"
case ":${PATH:-}:" in
	*:"$install_dir":*) ;;
	*) echo "Add $install_dir to PATH to run poorman." ;;
esac
