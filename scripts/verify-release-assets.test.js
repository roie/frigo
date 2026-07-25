const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { verifyRelease } = require("./verify-release-assets.js");

const triples = [
	"linux-x64",
	"linux-arm64",
	"win32-x64",
	"win32-arm64",
	"darwin-x64",
	"darwin-arm64",
];

function sha256(value) {
	return createHash("sha256").update(value).digest("hex");
}

function createReleaseFixture(t) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-verify-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const manifestPath = path.join(root, "checksums.json");
	const files = new Map();
	const manifest = {};

	for (const triple of triples) {
		const asset = `frigo-${triple}.gz`;
		const body = Buffer.from(`compressed-${triple}`);
		manifest[triple] = {
			asset,
			sha256: sha256(body),
			compressedSize: body.length,
			binarySha256: "a".repeat(64),
			binarySize: 1,
		};
		files.set(`/${asset}`, body);
	}
	const manifestBytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
	fs.writeFileSync(manifestPath, manifestBytes);
	files.set("/checksums.json", manifestBytes);
	return { files, manifestPath };
}

async function serve(t, files) {
	const server = http.createServer((request, response) => {
		const body = files.get(new URL(request.url, "http://127.0.0.1").pathname);
		if (!body) {
			response.writeHead(404).end();
			return;
		}
		response.writeHead(200, { "content-length": body.length });
		response.end(body);
	});
	await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
	t.after(() => new Promise((resolve) => server.close(resolve)));
	const address = server.address();
	return `http://127.0.0.1:${address.port}`;
}

test("verifyRelease checks remote manifest bytes and every asset", async (t) => {
	const fixture = createReleaseFixture(t);
	const baseUrl = await serve(t, fixture.files);
	assert.equal(await verifyRelease({ baseUrl, manifestPath: fixture.manifestPath }), 6);
});

test("verifyRelease rejects a mismatched remote checksums.json", async (t) => {
	const fixture = createReleaseFixture(t);
	fixture.files.set("/checksums.json", Buffer.from("{}\n"));
	const baseUrl = await serve(t, fixture.files);
	await assert.rejects(
		verifyRelease({ baseUrl, manifestPath: fixture.manifestPath }),
		/remote checksums.json does not match/,
	);
});

test("verifyRelease rejects an asset hash or size mismatch", async (t) => {
	const fixture = createReleaseFixture(t);
	fixture.files.set("/frigo-linux-x64.gz", Buffer.from("corrupt"));
	const baseUrl = await serve(t, fixture.files);
	await assert.rejects(
		verifyRelease({ baseUrl, manifestPath: fixture.manifestPath }),
		/linux-x64.*compressedSize|linux-x64.*sha256/,
	);
});

test("verifyRelease reports missing remote assets", async (t) => {
	const fixture = createReleaseFixture(t);
	fixture.files.delete("/frigo-darwin-arm64.gz");
	const baseUrl = await serve(t, fixture.files);
	await assert.rejects(
		verifyRelease({ baseUrl, manifestPath: fixture.manifestPath }),
		/darwin-arm64.*404/,
	);
});
