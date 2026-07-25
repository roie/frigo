const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const { execFileSync, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const script = path.join(__dirname, "build-release-assets.js");
const targets = {
	"linux-x64": "linux-x64/frigo",
	"linux-arm64": "linux-arm64/frigo",
	"win32-x64": "win32-x64/frigo.exe",
	"win32-arm64": "win32-arm64/frigo.exe",
	"darwin-x64": "darwin-x64/frigo",
	"darwin-arm64": "darwin-arm64/frigo",
};

function sha256(value) {
	return createHash("sha256").update(value).digest("hex");
}

function createFixture(t) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-assets-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const vendor = path.join(root, "vendor");
	const output = path.join(root, "release-assets");
	fs.mkdirSync(output, { recursive: true });

	for (const [triple, relative] of Object.entries(targets)) {
		const source = path.join(vendor, relative);
		fs.mkdirSync(path.dirname(source), { recursive: true });
		fs.writeFileSync(source, `binary-${triple}`);
	}

	execFileSync(process.execPath, [script, vendor, output]);
	return {
		manifestPath: path.join(output, "checksums.json"),
		output,
		vendor,
	};
}

test("build-release-assets records compressed and binary integrity", (t) => {
	const fixture = createFixture(t);
	const manifest = JSON.parse(fs.readFileSync(fixture.manifestPath, "utf8"));
	assert.deepEqual(Object.keys(manifest).sort(), Object.keys(targets).sort());

	for (const [triple, relative] of Object.entries(targets)) {
		const entry = manifest[triple];
		const binary = fs.readFileSync(path.join(fixture.vendor, relative));
		const compressed = fs.readFileSync(path.join(fixture.output, entry.asset));
		assert.equal(entry.binarySha256, sha256(binary));
		assert.equal(entry.binarySize, binary.length);
		assert.equal(entry.sha256, sha256(compressed));
		assert.equal(entry.compressedSize, compressed.length);
	}

	execFileSync(process.execPath, [script, "--verify-manifest", fixture.manifestPath]);
});

test("manifest verification rejects malformed metadata", (t) => {
	const fixture = createFixture(t);
	const manifest = JSON.parse(fs.readFileSync(fixture.manifestPath, "utf8"));
	delete manifest["linux-x64"].sha256;
	fs.writeFileSync(fixture.manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);

	const result = spawnSync(process.execPath, [script, "--verify-manifest", fixture.manifestPath], {
		encoding: "utf8",
	});
	assert.notEqual(result.status, 0);
	assert.match(result.stderr, /missing manifest field sha256 for linux-x64/);
});
