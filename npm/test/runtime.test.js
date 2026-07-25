const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const runtime = require("../bin/install.js");

const validEntry = {
	asset: "frigo-linux-x64.gz",
	sha256: "a".repeat(64),
	compressedSize: 10,
	binarySha256: "b".repeat(64),
	binarySize: 20,
};

test("targetFor maps all six supported targets", () => {
	assert.deepEqual(runtime.targetFor("linux", "x64"), {
		triple: "linux-x64",
		extension: "",
	});
	assert.deepEqual(runtime.targetFor("linux", "arm64"), {
		triple: "linux-arm64",
		extension: "",
	});
	assert.deepEqual(runtime.targetFor("darwin", "x64"), {
		triple: "darwin-x64",
		extension: "",
	});
	assert.deepEqual(runtime.targetFor("darwin", "arm64"), {
		triple: "darwin-arm64",
		extension: "",
	});
	assert.deepEqual(runtime.targetFor("win32", "x64"), {
		triple: "win32-x64",
		extension: ".exe",
	});
	assert.deepEqual(runtime.targetFor("win32", "arm64"), {
		triple: "win32-arm64",
		extension: ".exe",
	});
	assert.throws(() => runtime.targetFor("sunos", "riscv64"), /Unsupported platform.*sunos-riscv64/);
});

test("defaultCacheRoot follows host conventions", () => {
	assert.equal(runtime.defaultCacheRoot("linux", { XDG_CACHE_HOME: "/xdg" }, "/home/me"), "/xdg/frigo");
	assert.equal(runtime.defaultCacheRoot("linux", {}, "/home/me"), "/home/me/.cache/frigo");
	assert.equal(
		runtime.defaultCacheRoot("darwin", {}, "/Users/me"),
		"/Users/me/Library/Caches/frigo",
	);
	assert.equal(
		runtime.defaultCacheRoot("win32", { LOCALAPPDATA: "C:\\Local" }, "C:\\Users\\me"),
		"C:\\Local\\frigo\\Cache",
	);
	assert.equal(
		runtime.defaultCacheRoot("win32", {}, "C:\\Users\\me"),
		"C:\\Users\\me\\AppData\\Local\\frigo\\Cache",
	);
});

test("FRIGO_CACHE_DIR overrides the default cache", () => {
	assert.equal(
		runtime.defaultCacheRoot("linux", { FRIGO_CACHE_DIR: "./relative-cache" }, "/home/me"),
		path.resolve("relative-cache"),
	);
});

test("validateEntry accepts all five integrity fields", () => {
	assert.doesNotThrow(() => runtime.validateEntry(validEntry, "linux-x64"));
});

test("validateEntry requires binarySha256", () => {
	const entry = { ...validEntry };
	delete entry.binarySha256;
	assert.throws(() => runtime.validateEntry(entry, "linux-x64"), /binarySha256/);
});

test("validateEntry rejects unsafe asset names and malformed integrity", () => {
	assert.throws(
		() => runtime.validateEntry({ ...validEntry, asset: "../frigo-linux-x64.gz" }, "linux-x64"),
		/asset/,
	);
	assert.throws(
		() => runtime.validateEntry({ ...validEntry, sha256: "not-a-hash" }, "linux-x64"),
		/sha256/,
	);
	assert.throws(
		() => runtime.validateEntry({ ...validEntry, binarySize: 0 }, "linux-x64"),
		/binarySize/,
	);
});

test("lock release does not remove a replacement lock", async (t) => {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-lock-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const lockPath = path.join(root, "binary.lock");
	const release = await runtime.acquireLock(lockPath);
	fs.writeFileSync(lockPath, "replacement-owner\n");
	await release();
	assert.equal(fs.readFileSync(lockPath, "utf8"), "replacement-owner\n");
});

async function listen(t, server) {
	await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
	t.after(() => new Promise((resolve) => server.close(resolve)));
	return server.address().port;
}

test("requestWithRedirects follows redirects and enforces size", async (t) => {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-download-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const body = Buffer.from("downloaded-binary");
	const server = http.createServer((request, response) => {
		if (request.url === "/redirect") {
			response.writeHead(302, { location: "/asset" }).end();
			return;
		}
		response.writeHead(200, { "content-length": body.length }).end(body);
	});
	const port = await listen(t, server);
	const destination = path.join(root, "asset.gz");
	await runtime.requestWithRedirects(`http://127.0.0.1:${port}/redirect`, destination, body.length);
	assert.deepEqual(fs.readFileSync(destination), body);
});

test("requestWithRedirects enforces a wall-clock timeout", async (t) => {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-timeout-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const server = http.createServer((_request, response) => {
		response.writeHead(200, { "content-length": 4 });
		response.write("a");
		const interval = setInterval(() => response.write("b"), 20);
		setTimeout(() => {
			clearInterval(interval);
			response.end("c");
		}, 100);
	});
	const port = await listen(t, server);
	await assert.rejects(
		runtime.requestWithRedirects(
			`http://127.0.0.1:${port}/slow`,
			path.join(root, "asset.gz"),
			4,
			10,
			50,
		),
		/timed out after 50ms/,
	);
});

test("requestWithRedirects honors HTTP_PROXY", async (t) => {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-proxy-test-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const body = Buffer.from("proxied-binary");
	const target = http.createServer((_request, response) => {
		response.writeHead(200, { "content-length": body.length }).end(body);
	});
	const targetPort = await listen(t, target);
	let connectCount = 0;
	const proxy = http.createServer();
	proxy.on("connect", (request, client, head) => {
		connectCount += 1;
		const [host, port] = request.url.split(":");
		const upstream = net.connect(Number(port), host, () => {
			client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
			if (head.length > 0) upstream.write(head);
			upstream.pipe(client);
			client.pipe(upstream);
		});
	});
	const proxyPort = await listen(t, proxy);
	const previous = {
		HTTP_PROXY: process.env.HTTP_PROXY,
		NO_PROXY: process.env.NO_PROXY,
		http_proxy: process.env.http_proxy,
		no_proxy: process.env.no_proxy,
	};
	t.after(() => {
		for (const [key, value] of Object.entries(previous)) {
			if (value === undefined) delete process.env[key];
			else process.env[key] = value;
		}
	});
	process.env.HTTP_PROXY = `http://127.0.0.1:${proxyPort}`;
	delete process.env.http_proxy;
	process.env.NO_PROXY = "";
	delete process.env.no_proxy;

	const destination = path.join(root, "asset.gz");
	await runtime.requestWithRedirects(
		`http://127.0.0.1:${targetPort}/asset`,
		destination,
		body.length,
	);
	assert.deepEqual(fs.readFileSync(destination), body);
	assert.equal(connectCount, 1);
});
