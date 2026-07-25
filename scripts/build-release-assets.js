#!/usr/bin/env node

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const zlib = require("node:zlib");

const TARGETS = [
	{
		triple: "linux-x64",
		source: "linux-x64/frigo",
		asset: "frigo-linux-x64.gz",
	},
	{
		triple: "linux-arm64",
		source: "linux-arm64/frigo",
		asset: "frigo-linux-arm64.gz",
	},
	{
		triple: "win32-x64",
		source: "win32-x64/frigo.exe",
		asset: "frigo-win32-x64.gz",
	},
	{
		triple: "win32-arm64",
		source: "win32-arm64/frigo.exe",
		asset: "frigo-win32-arm64.gz",
	},
	{
		triple: "darwin-x64",
		source: "darwin-x64/frigo",
		asset: "frigo-darwin-x64.gz",
	},
	{
		triple: "darwin-arm64",
		source: "darwin-arm64/frigo",
		asset: "frigo-darwin-arm64.gz",
	},
];

const HASH_PATTERN = /^[a-f0-9]{64}$/;
const MANIFEST_FIELDS = ["asset", "sha256", "compressedSize", "binarySha256", "binarySize"];

function usage() {
	const scriptName = path.basename(process.argv[1]);
	process.stderr.write(
		`usage: ${scriptName} <binary-root> <output-dir>\n` +
			`       ${scriptName} --verify-manifest <checksums-json>\n`,
	);
	process.exit(1);
}

function sha256(buffer) {
	return crypto.createHash("sha256").update(buffer).digest("hex");
}

function fail(message) {
	throw new Error(message);
}

function readManifest(manifestPath) {
	let manifest;
	try {
		manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
	} catch (error) {
		fail(`invalid release manifest ${manifestPath}: ${error.message}`);
	}
	return manifest;
}

function verifyManifest(manifest) {
	if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) {
		fail("release manifest must be a JSON object");
	}

	const expectedTriples = TARGETS.map((target) => target.triple).sort();
	const actualTriples = Object.keys(manifest).sort();
	if (JSON.stringify(actualTriples) !== JSON.stringify(expectedTriples)) {
		fail(
			`release manifest targets must be exactly ${expectedTriples.join(", ")}; got ${actualTriples.join(", ")}`,
		);
	}

	const seenAssets = new Set();
	for (const target of TARGETS) {
		const entry = manifest[target.triple];
		if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
			fail(`invalid manifest entry for ${target.triple}`);
		}
		for (const field of MANIFEST_FIELDS) {
			if (!Object.prototype.hasOwnProperty.call(entry, field)) {
				fail(`missing manifest field ${field} for ${target.triple}`);
			}
		}
		if (entry.asset !== target.asset || path.basename(entry.asset) !== entry.asset) {
			fail(`invalid asset for ${target.triple}: ${entry.asset}`);
		}
		if (seenAssets.has(entry.asset)) {
			fail(`duplicate release asset: ${entry.asset}`);
		}
		seenAssets.add(entry.asset);
		for (const hashField of ["sha256", "binarySha256"]) {
			if (typeof entry[hashField] !== "string" || !HASH_PATTERN.test(entry[hashField])) {
				fail(`invalid manifest field ${hashField} for ${target.triple}`);
			}
		}
		for (const sizeField of ["compressedSize", "binarySize"]) {
			if (!Number.isSafeInteger(entry[sizeField]) || entry[sizeField] <= 0) {
				fail(`invalid manifest field ${sizeField} for ${target.triple}`);
			}
		}
	}

	return manifest;
}

function buildReleaseAssets(binaryRoot, outputDir) {
	for (const currentPath of [binaryRoot, outputDir]) {
		if (!fs.existsSync(currentPath) || !fs.statSync(currentPath).isDirectory()) {
			fail(`missing directory: ${currentPath}`);
		}
	}

	const manifest = {};
	for (const target of TARGETS) {
		const binaryPath = path.join(binaryRoot, target.source);
		const assetPath = path.join(outputDir, target.asset);
		const binary = fs.readFileSync(binaryPath);
		const compressed = zlib.gzipSync(binary, { level: 9, mtime: 0 });

		fs.writeFileSync(assetPath, compressed);
		manifest[target.triple] = {
			asset: target.asset,
			sha256: sha256(compressed),
			compressedSize: compressed.length,
			binarySha256: sha256(binary),
			binarySize: binary.length,
		};
	}

	verifyManifest(manifest);
	const manifestPath = path.join(outputDir, "checksums.json");
	fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
	return manifestPath;
}

function main() {
	const args = process.argv.slice(2);
	if (args[0] === "--verify-manifest") {
		if (args.length !== 2) usage();
		verifyManifest(readManifest(args[1]));
		return;
	}
	if (args.length !== 2) usage();
	buildReleaseAssets(args[0], args[1]);
}

if (require.main === module) {
	try {
		main();
	} catch (error) {
		process.stderr.write(`${error.message}\n`);
		process.exit(1);
	}
}

module.exports = { MANIFEST_FIELDS, TARGETS, buildReleaseAssets, readManifest, verifyManifest };
