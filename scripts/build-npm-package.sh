#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: %s <version> <output-dir> <checksums-json>\n' "${0##*/}" >&2
	exit 1
}

if [ "$#" -ne 3 ]; then
	usage
fi

version=$1
outdir=$2
checksums_path=$3

if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
	printf 'invalid semantic version: %s\n' "$version" >&2
	exit 1
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
template="$repo_root/npm/package.json.tmpl"
launcher="$repo_root/npm/bin/frigo.js"
readme="$repo_root/README.md"
license="$repo_root/LICENSE"
installer="$repo_root/npm/bin/install.js"

for required in "$template" "$launcher" "$installer" "$checksums_path" "$readme" "$license"; do
	if [ ! -f "$required" ]; then
		printf 'missing required packaging source: %s\n' "$required" >&2
		exit 1
	fi
done

mkdir -p "$outdir/bin"
rendered_package_json="$outdir/package.json"
tmp_package_json="$outdir/.package.json.tmp"
sed "s/@@VERSION@@/$version/g" "$template" >"$tmp_package_json"
mv "$tmp_package_json" "$rendered_package_json"
cp "$launcher" "$outdir/bin/frigo.js"
chmod +x "$outdir/bin/frigo.js"
cp "$installer" "$outdir/bin/install.js"
chmod +x "$outdir/bin/install.js"
cp "$readme" "$outdir/README.md"
cp "$license" "$outdir/LICENSE"
cp "$checksums_path" "$outdir/checksums.json"
