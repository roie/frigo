#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
case "$#" in
	0) version=0.2.0; use_prebuilt=false ;;
	1) version=$1; use_prebuilt=false ;;
	3)
		version=$1
		use_prebuilt=true
		provided_tarball=$(cd -- "$(dirname -- "$2")" && pwd)/$(basename -- "$2")
		provided_release_assets=$(cd -- "$3" && pwd)
		;;
	*)
		printf 'usage: %s [version [tarball release-assets-dir]]\n' "${0##*/}" >&2
		exit 1
		;;
esac
workdir=$(mktemp -d)
server_pid=

stop_server() {
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
		server_pid=
	fi
}

cleanup() {
	stop_server
	rm -rf "$workdir"
}
trap cleanup EXIT

if [ "$use_prebuilt" = false ]; then
stage="$workdir/package"
release_assets="$stage/release-assets"
mkdir -p \
	"$stage/vendor/linux-x64" \
	"$stage/vendor/linux-arm64" \
	"$stage/vendor/win32-x64" \
	"$stage/vendor/win32-arm64" \
	"$stage/vendor/darwin-x64" \
	"$stage/vendor/darwin-arm64" \
	"$release_assets"

ldflags="-s -w -X github.com/roie/frigo/internal/cli.version=${version}"
GOFLAGS=-mod=readonly GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/linux-x64/frigo" "$repo_root/cmd/frigo"
GOFLAGS=-mod=readonly GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/linux-arm64/frigo" "$repo_root/cmd/frigo"
GOFLAGS=-mod=readonly GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/win32-x64/frigo.exe" "$repo_root/cmd/frigo"
GOFLAGS=-mod=readonly GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/win32-arm64/frigo.exe" "$repo_root/cmd/frigo"
GOFLAGS=-mod=readonly GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/darwin-x64/frigo" "$repo_root/cmd/frigo"
GOFLAGS=-mod=readonly GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/vendor/darwin-arm64/frigo" "$repo_root/cmd/frigo"

assert_target() {
	local binary=$1
	local expected=$2
	local description
	description=$(file -b "$binary")
	if [[ "$description" != *"$expected"* ]]; then
		printf 'unexpected target for %s\nexpected: %s\nactual: %s\n' "$binary" "$expected" "$description" >&2
		exit 1
	fi
}

assert_target "$stage/vendor/linux-x64/frigo" "ELF 64-bit LSB executable, x86-64"
assert_target "$stage/vendor/linux-arm64/frigo" "ELF 64-bit LSB executable, ARM aarch64"
assert_target "$stage/vendor/win32-x64/frigo.exe" "PE32+ executable (console) x86-64"
assert_target "$stage/vendor/win32-arm64/frigo.exe" "PE32+ executable (console) Aarch64"
assert_target "$stage/vendor/darwin-x64/frigo" "Mach-O 64-bit x86_64 executable"
assert_target "$stage/vendor/darwin-arm64/frigo" "Mach-O 64-bit arm64 executable"

node "$repo_root/scripts/build-release-assets.js" "$stage/vendor" "$release_assets"
node "$repo_root/scripts/build-release-assets.js" --verify-manifest "$release_assets/checksums.json"

"$repo_root/scripts/build-npm-package.sh" "$version" \
	"$stage/npm" \
	"$release_assets/checksums.json"

cd "$stage/npm"
tarball=$(npm pack --silent)
tarball_path="$stage/npm/$tarball"
else
	tarball_path=$provided_tarball
	release_assets=$provided_release_assets
	if [ ! -f "$tarball_path" ]; then
		printf 'missing npm tarball: %s\n' "$tarball_path" >&2
		exit 1
	fi
	node "$repo_root/scripts/build-release-assets.js" --verify-manifest "$release_assets/checksums.json"
fi

expected_files=$(
	cat <<'EOF'
package/LICENSE
package/README.md
package/bin/frigo.js
package/bin/install.js
package/checksums.json
package/package.json
EOF
)
actual_files=$(tar -tzf "$tarball_path" | sort)
if [ "$actual_files" != "$expected_files" ]; then
	printf 'unexpected tarball contents\nexpected:\n%s\nactual:\n%s\n' "$expected_files" "$actual_files" >&2
	exit 1
fi

install_dir="$workdir/install"
set +e
install_output=$(npm install --prefix "$install_dir" "$tarball_path" --no-audit --no-fund 2>&1)
install_status=$?
set -e
if [ "$install_status" -ne 0 ]; then
	printf 'npm package installation failed\n%s\n' "$install_output" >&2
	exit 1
fi
if [[ "$install_output" == *"install scripts blocked"* ]]; then
	printf 'npm blocked a required install script\n%s\n' "$install_output" >&2
	exit 1
fi

pkg_dir="$install_dir/node_modules/frigo"
frigo_bin="$install_dir/node_modules/.bin/frigo"
if [ "$(node -p "require('$pkg_dir/package.json').version")" != "$version" ]; then
	printf 'package.json version mismatch\n' >&2
	exit 1
fi
node - "$pkg_dir/package.json" <<'NODE'
const manifest = require(process.argv[2]);
if (manifest.scripts && Object.keys(manifest.scripts).length > 0) {
	throw new Error(`unexpected lifecycle scripts: ${JSON.stringify(manifest.scripts)}`);
}
NODE
if [ ! -x "$frigo_bin" ]; then
	printf 'launcher is not executable\n' >&2
	exit 1
fi
if [ ! -f "$pkg_dir/checksums.json" ] || [ ! -f "$pkg_dir/bin/install.js" ]; then
	printf 'runtime package files are missing\n' >&2
	exit 1
fi
if ! cmp "$pkg_dir/checksums.json" "$release_assets/checksums.json"; then
	printf 'npm package manifest does not match release assets\n' >&2
	exit 1
fi

NODE_PATH="$install_dir/node_modules" node --test "$repo_root/npm/test/runtime.test.js"

port_file="$workdir/release-server.port"
start_server() {
	rm -f "$port_file"
	node "$repo_root/scripts/serve-release-assets.js" "$release_assets" "$port_file" &
	server_pid=$!
	for _ in $(seq 1 100); do
		if [ -s "$port_file" ]; then
			release_base_url="http://127.0.0.1:$(cat "$port_file")"
			return
		fi
		if ! kill -0 "$server_pid" 2>/dev/null; then
			wait "$server_pid"
			printf 'release asset server exited before publishing its port\n' >&2
			exit 1
		fi
		sleep 0.05
	done
	printf 'timed out waiting for release asset server\n' >&2
	exit 1
}

cache_dir="$workdir/cache"
triple=$(node -p '`${process.platform}-${process.arch}`')
case "$triple" in
	linux-x64|linux-arm64|darwin-x64|darwin-arm64) cached_name=frigo ;;
	win32-x64|win32-arm64) cached_name=frigo.exe ;;
	*) printf 'package smoke test does not support host %s\n' "$triple" >&2; exit 1 ;;
esac
target_cache_dir="$cache_dir/$version/$triple"
cached_binary="$target_cache_dir/$cached_name"

start_server
first_output=$(FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version)
if [ "$first_output" != "frigo $version" ]; then
	printf 'unexpected first-run version output: %s\n' "$first_output" >&2
	exit 1
fi
if [ ! -f "$cached_binary" ]; then
	printf 'first run did not cache %s\n' "$cached_binary" >&2
	exit 1
fi

stop_server
cached_output=$(FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="http://127.0.0.1:1" "$frigo_bin" --version)
if [ "$cached_output" != "frigo $version" ]; then
	printf 'unexpected offline cached version output: %s\n' "$cached_output" >&2
	exit 1
fi

start_server
printf 'corrupt' >"$cached_binary"
recovered_output=$(FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version)
if [ "$recovered_output" != "frigo $version" ]; then
	printf 'corrupt cache was not recovered: %s\n' "$recovered_output" >&2
	exit 1
fi

cp "$pkg_dir/checksums.json" "$workdir/checksums.json.original"
node - "$pkg_dir/checksums.json" "$triple" <<'NODE'
const fs = require('node:fs');
const [manifestPath, triple] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
manifest[triple].sha256 = '0'.repeat(64);
fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
rm -rf "$target_cache_dir"
set +e
checksum_output=$(FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version 2>&1)
checksum_status=$?
set -e
if [ "$checksum_status" -eq 0 ] || [[ "$checksum_output" != *"Checksum mismatch"* ]]; then
	printf 'checksum mismatch was not rejected\n%s\n' "$checksum_output" >&2
	exit 1
fi
if [ -e "$cached_binary" ]; then
	printf 'checksum failure left a final cached binary\n' >&2
	exit 1
fi
cp "$workdir/checksums.json.original" "$pkg_dir/checksums.json"
node - "$pkg_dir/checksums.json" "$triple" <<'NODE'
const fs = require('node:fs');
const [manifestPath, triple] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
manifest[triple].binarySha256 = '0'.repeat(64);
fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
rm -rf "$target_cache_dir"
set +e
binary_checksum_output=$(FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version 2>&1)
binary_checksum_status=$?
set -e
if [ "$binary_checksum_status" -eq 0 ] || [[ "$binary_checksum_output" != *"Binary checksum mismatch"* ]]; then
	printf 'binary checksum mismatch was not rejected\n%s\n' "$binary_checksum_output" >&2
	exit 1
fi
if [ -e "$cached_binary" ]; then
	printf 'binary checksum failure left a final cached binary\n' >&2
	exit 1
fi
mv "$workdir/checksums.json.original" "$pkg_dir/checksums.json"

rm -rf "$target_cache_dir"
set +e
FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version >"$workdir/concurrent-1.out" 2>"$workdir/concurrent-1.err" &
pid_one=$!
FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version >"$workdir/concurrent-2.out" 2>"$workdir/concurrent-2.err" &
pid_two=$!
wait "$pid_one"
status_one=$?
wait "$pid_two"
status_two=$?
set -e
if [ "$status_one" -ne 0 ] || [ "$status_two" -ne 0 ]; then
	printf 'concurrent launch failed\n%s\n%s\n' "$(cat "$workdir/concurrent-1.err")" "$(cat "$workdir/concurrent-2.err")" >&2
	exit 1
fi
if [ "$(cat "$workdir/concurrent-1.out")" != "frigo $version" ] || [ "$(cat "$workdir/concurrent-2.out")" != "frigo $version" ]; then
	printf 'concurrent launch returned unexpected output\n' >&2
	exit 1
fi
shopt -s nullglob
leftovers=("$target_cache_dir"/.*.lock "$target_cache_dir"/.*.tmp "$target_cache_dir"/.*.download)
shopt -u nullglob
if [ "${#leftovers[@]}" -ne 0 ]; then
	printf 'runtime cache left temporary files: %s\n' "${leftovers[*]}" >&2
	exit 1
fi

if [ -d /dev/shm ] && [ "$(stat -c %d /dev/shm 2>/dev/null || true)" != "$(stat -c %d "$workdir" 2>/dev/null || true)" ]; then
	rm -rf "$target_cache_dir"
	split_output=$(TMPDIR=/dev/shm FRIGO_CACHE_DIR="$cache_dir" FRIGO_RELEASE_BASE_URL="$release_base_url" "$frigo_bin" --version)
	if [ "$split_output" != "frigo $version" ]; then
		printf 'split-filesystem runtime failed: %s\n' "$split_output" >&2
		exit 1
	fi
fi

printf 'package smoke test passed\n'
