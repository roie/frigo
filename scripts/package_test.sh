#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

version=0.2.0
stage="$workdir/package"
mkdir -p \
	"$stage/bin" \
	"$stage/vendor/linux-x64" \
	"$stage/vendor/linux-arm64" \
	"$stage/vendor/win32-x64" \
	"$stage/vendor/win32-arm64" \
	"$stage/vendor/darwin-x64" \
	"$stage/vendor/darwin-arm64"

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

"$repo_root/scripts/build-npm-package.sh" "$version" "$stage"

cd "$stage"
tarball=$(npm pack)

expected_files=$(
	cat <<'EOF'
package/LICENSE
package/README.md
package/bin/frigo.js
package/package.json
package/vendor/darwin-arm64/frigo
package/vendor/darwin-x64/frigo
package/vendor/linux-arm64/frigo
package/vendor/linux-x64/frigo
package/vendor/win32-arm64/frigo.exe
package/vendor/win32-x64/frigo.exe
EOF
)
actual_files=$(tar -tzf "$tarball" | sort)
if [ "$actual_files" != "$expected_files" ]; then
	printf 'unexpected tarball contents\nexpected:\n%s\nactual:\n%s\n' "$expected_files" "$actual_files" >&2
	exit 1
fi

install_dir="$workdir/install"
npm install --prefix "$install_dir" "$stage/$tarball" >/dev/null
pkg_dir="$install_dir/node_modules/frigo"
frigo_bin="$install_dir/node_modules/.bin/frigo"

if [ "$(node -p "require('$pkg_dir/package.json').version")" != "$version" ]; then
	printf 'package.json version mismatch\n' >&2
	exit 1
fi

if [ ! -x "$frigo_bin" ]; then
	printf 'launcher is not executable\n' >&2
	exit 1
fi

if [ "$("$frigo_bin" --version)" != "frigo $version" ]; then
	printf 'unexpected linux amd64 version output\n' >&2
	exit 1
fi

unsupported_output=$(
	node -e '
const launcher = process.argv[1];
Object.defineProperty(process, "platform", { value: "sunos" });
Object.defineProperty(process, "arch", { value: "riscv64" });
require(launcher);
' "$pkg_dir/bin/frigo.js" 2>&1 || true
)
if [[ "$unsupported_output" != *"frigo binary not available for sunos-riscv64"* ]]; then
	printf 'missing unsupported-platform guidance\n%s\n' "$unsupported_output" >&2
	exit 1
fi

printf 'package smoke test passed\n'
