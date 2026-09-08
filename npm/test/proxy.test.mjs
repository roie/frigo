import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import test from "node:test";

assert.ok(process.env.FRIGO_TEST_PACKAGE_ROOT, "test the installed package");
const certificate = path.join(import.meta.dirname, "fixtures/proxy-cert.pem");
// Local test-only identity, never used by the runtime or included in the package.
const tlsOptions = {
	key: fs.readFileSync(path.join(import.meta.dirname, "fixtures/proxy-key.pem")),
	cert: fs.readFileSync(certificate),
};
// Node #62333 landed in 24.16.0 and 26.1.0; 25.x retains broad suffix matching.
// Characterize that real native boundary, not a wrapper-specific matcher or skip.
const [nodeMajor, nodeMinor] = process.versions.node.split(".").map(Number);
const dotBoundary =
	(nodeMajor === 24 && nodeMinor >= 16) ||
	(nodeMajor === 26 && nodeMinor >= 1) ||
	nodeMajor > 26;
const body = Buffer.from([0, 255, 13, 10, 0, 128, 65, 66]);

function isolatedEnv(overrides = {}) {
	const env = { ...process.env };
	// Windows environment keys are case-insensitive. Delete every spelling before
	// adding overrides instead of clearing one case and accidentally retaining another.
	for (const key of Object.keys(env)) {
		if (
			/^(.*proxy.*|node_options|node_extra_ca_certs|node_tls_reject_unauthorized)$/i.test(
				key,
			)
		) {
			delete env[key];
		}
	}
	return { ...env, NODE_EXTRA_CA_CERTS: certificate, ...overrides };
}

async function listen(t, server) {
	const sockets = new Set();
	server.on("connection", (socket) => {
		sockets.add(socket);
		socket.on("close", () => sockets.delete(socket));
	});
	await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
	t.after(() => {
		for (const socket of sockets) socket.destroy();
		return new Promise((resolve) => server.close(resolve));
	});
	return server.address().port;
}

async function fixture(
	t,
	targetProtocol = "http",
	proxyProtocol = "http",
	options = {},
) {
	const targetRequests = [];
	const connections = { target: 0, proxy: 0 };
	const target = (targetProtocol === "https" ? https : http).createServer(
		targetProtocol === "https" ? tlsOptions : {},
		(request, response) => {
			targetRequests.push({ url: request.url, headers: request.headers });
			if (options.target) return options.target(request, response);
			response.writeHead(200, { "content-length": body.length }).end(body);
		},
	);
	target.on("connection", () => connections.target++);
	const targetPort = await listen(t, target);
	const requests = [];
	const proxy = (proxyProtocol === "https" ? https : http).createServer(
		proxyProtocol === "https" ? tlsOptions : {},
	);
	proxy.on("connection", () => connections.proxy++);
	function authorize(request, response) {
		requests.push({ url: request.url, headers: request.headers });
		if (options.stall) return false;
		if (
			options.reject ||
			(options.auth && request.headers["proxy-authorization"] !== options.auth)
		) {
			if (response.writeHead) response.writeHead(407).end();
			else
				response.end(
					"HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n",
				);
			return false;
		}
		return true;
	}
	// Accept both standard HTTP forwarding and CONNECT: transport is not a CLI contract.
	proxy.on("request", (request, response) => {
		if (!authorize(request, response)) return;
		const url = new URL(request.url);
		const headers = { ...request.headers };
		delete headers["proxy-authorization"];
		delete headers["proxy-connection"];
		const upstream = http.request(url, { headers, agent: false }, (incoming) => {
			response.writeHead(incoming.statusCode, incoming.headers);
			incoming.pipe(response);
		});
		upstream.on("error", () => response.destroy());
		response.on("close", () => upstream.destroy());
		request.pipe(upstream);
	});
	proxy.on("connect", (request, client, head) => {
		if (!authorize(request, client)) return;
		const url = new URL(`http://${request.url}`);
		const upstream = net.connect(Number(url.port), url.hostname, () => {
			client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
			if (head.length) upstream.write(head);
			upstream.pipe(client);
			client.pipe(upstream);
		});
		upstream.on("error", () => client.destroy());
		client.on("error", () => upstream.destroy());
		client.on("close", () => upstream.destroy());
		t.after(() => upstream.destroy());
	});
	const proxyPort = await listen(t, proxy);
	return {
		url: `${targetProtocol}://127.0.0.1:${targetPort}/asset?exact=%00`,
		proxy: `${proxyProtocol}://127.0.0.1:${proxyPort}`,
		targetPort,
		requests,
		targetRequests,
		connections,
	};
}

async function download(
	t,
	url,
	env = {},
	{ timeout = 1000, retries = false } = {},
) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "frigo-proxy-"));
	t.after(() => fs.rmSync(root, { recursive: true, force: true }));
	const destination = path.join(root, "download");
	const source = `
		import { pathToFileURL } from 'node:url';
		const runtime = await import(pathToFileURL(process.argv[1]));
		try {
			${retries ? "await runtime.downloadWithRetries(process.argv[2], process.argv[3], 8, {attemptTimeoutMs: Number(process.argv[4]), totalTimeoutMs: Number(process.argv[4]), maxRetries: 3, retryDelayMs: 20});" : "await runtime.requestWithRedirects(process.argv[2], process.argv[3], 8, 10, Number(process.argv[4]));"}
		} catch (error) { console.error(error.message); process.exitCode = 1; }
	`;
	const child = spawn(
		process.execPath,
		[
			"--input-type=module",
			"-e",
			source,
			path.join(process.env.FRIGO_TEST_PACKAGE_ROOT, "bin/install.js"),
			url,
			destination,
			String(timeout),
		],
		{ env: isolatedEnv(env), stdio: ["ignore", "pipe", "pipe"] },
	);
	let stderr = "";
	child.stderr.on("data", (chunk) => {
		stderr += chunk;
	});
	let killed = false;
	const watchdog = setTimeout(() => {
		killed = true;
		child.kill();
	}, timeout + 3000);
	const code = await new Promise((resolve, reject) => {
		child.on("error", reject);
		child.on("close", resolve);
	}).finally(() => clearTimeout(watchdog));
	assert.equal(
		killed,
		false,
		`download process exceeded its deadline: ${stderr}`,
	);
	return {
		code,
		stderr,
		bytes: fs.existsSync(destination) ? fs.readFileSync(destination) : undefined,
	};
}

function success(result) {
	assert.equal(result.code, 0, result.stderr);
	assert.deepEqual(result.bytes, body);
}

for (const targetProtocol of ["http", "https"]) {
	for (const proxyProtocol of ["http", "https"]) {
		test(`${targetProtocol} target via ${proxyProtocol} proxy preserves bytes and credentials`, async (t) => {
			const auth = `Basic ${Buffer.from("user@:p:ss").toString("base64")}`;
			const f = await fixture(t, targetProtocol, proxyProtocol, { auth });
			const proxy = new URL(f.proxy);
			proxy.username = "user@";
			proxy.password = "p:ss";
			success(
				await download(t, f.url, {
					[`${targetProtocol.toUpperCase()}_PROXY`]: proxy.href,
				}),
			);
			assert.equal(f.requests.length, 1);
			assert.equal(f.requests[0].headers["proxy-authorization"], auth);
			assert.equal(f.targetRequests[0].headers["proxy-authorization"], undefined);
			assert.equal(f.targetRequests[0].url, "/asset?exact=%00");
		});
	}
	for (const proxyProtocol of ["http", "https"]) {
		for (const variable of [
			`${targetProtocol.toUpperCase()}_PROXY`,
			"ALL_PROXY",
		]) {
			for (const scheme of [
				proxyProtocol.toUpperCase(),
				proxyProtocol === "http" ? "HtTp" : "HtTpS",
			]) {
				test(`${variable} ${scheme} scheme routes ${targetProtocol} targets without leaking auth`, async (t) => {
					const auth = `Basic ${Buffer.from("review-user:review-secret").toString("base64")}`;
					const f = await fixture(t, targetProtocol, proxyProtocol, { auth });
					const proxy = new URL(f.proxy);
					proxy.username = "review-user";
					proxy.password = "review-secret";
					success(
						await download(t, f.url, {
							[variable]: proxy.href.replace(`${proxyProtocol}:`, `${scheme}:`),
						}),
					);
					assert.equal(f.requests.length, 1);
					assert.equal(f.requests[0].headers["proxy-authorization"], auth);
					assert.equal(f.targetRequests.length, 1);
					assert.equal(
						f.targetRequests[0].headers["proxy-authorization"],
						undefined,
					);
				});
			}
			for (const [name, corrupt] of [
				["trailing CR", (url) => `${url}\r`],
				["trailing LF", (url) => `${url}\n`],
				["embedded CR", (url) => url.replace("review-user", "review-\ruser")],
				["embedded LF", (url) => url.replace("review-secret", "review-\nsecret")],
			]) {
				test(`${variable} ${proxyProtocol} credential-bearing ${name} is rejected before traffic`, async (t) => {
					const f = await fixture(t, targetProtocol, proxyProtocol);
					const proxy = new URL(f.proxy);
					proxy.username = "review-user";
					proxy.password = "review-secret";
					const result = await download(t, f.url, {
						[variable]: corrupt(proxy.href),
					});
					assert.equal(result.code, 1);
					assert.doesNotMatch(result.stderr, /review-|user|secret/);
					assert.match(result.stderr, /Invalid frigo proxy URL/);
					assert.deepEqual(f.connections, { target: 0, proxy: 0 });
					assert.equal(f.requests.length, 0);
					assert.equal(f.targetRequests.length, 0);
				});
			}
		}
	}
	for (const proxyProtocol of ["http", "https"]) {
		for (const [name, corrupt] of [
			["invalid host", (url) => url.replace("127.0.0.1", "[invalid")],
			["invalid port", (url) => url.replace(/:\d+\/$/, ":invalid/")],
			[
				"invalid credential encoding",
				(url) => url.replace("review-user", "review-user%zz"),
			],
		]) {
			test(`${targetProtocol} malformed ${proxyProtocol} proxy ${name} fails without credentials or traffic`, async (t) => {
				const f = await fixture(t, targetProtocol, proxyProtocol);
				const proxy = new URL(f.proxy);
				proxy.username = "review-user";
				proxy.password = "review-secret";
				const result = await download(t, f.url, { ALL_PROXY: corrupt(proxy.href) });
				assert.equal(result.code, 1);
				assert.doesNotMatch(result.stderr, /review-|user|secret/);
				assert.deepEqual(f.connections, { target: 0, proxy: 0 });
				assert.equal(f.requests.length, 0);
				assert.equal(f.targetRequests.length, 0);
			});
		}
	}
	for (const variable of ["ALL_PROXY", "all_proxy", `${targetProtocol}_proxy`]) {
		test(`${variable} routes ${targetProtocol} targets`, async (t) => {
			const f = await fixture(t, targetProtocol);
			success(await download(t, f.url, { [variable]: f.proxy }));
			assert.equal(f.requests.length, 1);
		});
	}
	test(`${targetProtocol} scheme-less proxy defaults to target protocol`, async (t) => {
		const f = await fixture(t, targetProtocol, targetProtocol);
		success(await download(t, f.url, { ALL_PROXY: new URL(f.proxy).host }));
		assert.equal(f.requests.length, 1);
	});
	test(`${targetProtocol} proxy takes precedence over ALL_PROXY`, async (t) => {
		const f = await fixture(t, targetProtocol);
		success(
			await download(t, f.url, {
				[`${targetProtocol.toUpperCase()}_PROXY`]: f.proxy,
				ALL_PROXY: "http://127.0.0.1:1",
			}),
		);
		assert.equal(f.requests.length, 1);
	});
	test(`${targetProtocol} npm proxy variables remain ignored`, async (t) => {
		const f = await fixture(t, targetProtocol);
		success(
			await download(t, f.url, {
				npm_config_proxy: "http://127.0.0.1:1",
				npm_config_http_proxy: "http://127.0.0.1:1",
				npm_config_https_proxy: "http://127.0.0.1:1",
			}),
		);
		assert.equal(f.requests.length, 0);
	});
	for (const [name, value, bypass] of [
		["exact host", () => "127.0.0.1", true],
		["all hosts", () => "*", true],
		["matching port", (f) => `127.0.0.1:${f.targetPort}`, true],
		["different port", () => "127.0.0.1:1", false],
		["suffix", () => ".0.0.1", true],
		[
			`native leading-dot boundary on Node ${process.versions.node}`,
			() => ".7.0.0.1",
			!dotBoundary,
		],
		["wildcard suffix", () => "*.0.0.1", true],
		["unmatched", () => "example.invalid", false],
		["comma-separated", () => "example.invalid,127.0.0.1", true],
		["space-separated is not a list", () => "example.invalid 127.0.0.1", false],
		["suffix with port is not supported", (f) => `.0.0.1:${f.targetPort}`, false],
		["bare wildcard suffix is not supported", () => "*0.0.1", false],
		["suffix includes apex", () => ".127.0.0.1", true],
		["IP range", () => "127.0.0.1-127.0.0.2", true],
	]) {
		test(`${targetProtocol} NO_PROXY ${name}`, async (t) => {
			const f = await fixture(t, targetProtocol);
			success(
				await download(t, f.url, { ALL_PROXY: f.proxy, NO_PROXY: value(f) }),
			);
			assert.equal(f.requests.length, bypass ? 0 : 1);
		});
	}
	for (const variable of [`${targetProtocol}_proxy`, "all_proxy", "no_proxy"]) {
		test(`${variable} nonempty lowercase takes precedence`, {
			skip: process.platform === "win32",
		}, async (t) => {
			const f = await fixture(t, targetProtocol);
			const env =
				variable === "no_proxy"
					? { ALL_PROXY: f.proxy, NO_PROXY: "example.invalid", no_proxy: "*" }
					: { [variable.toUpperCase()]: "http://127.0.0.1:1", [variable]: f.proxy };
			success(await download(t, f.url, env));
			assert.equal(f.requests.length, variable === "no_proxy" ? 0 : 1);
		});
	}
	test(`${targetProtocol} ignores npm_config_no_proxy`, async (t) => {
		const f = await fixture(t, targetProtocol);
		success(
			await download(t, f.url, { ALL_PROXY: f.proxy, npm_config_no_proxy: "*" }),
		);
		assert.equal(f.requests.length, 1);
	});
	test(`${targetProtocol} rejects untrusted TLS`, async (t) => {
		const f = await fixture(t, targetProtocol, "https");
		const result = await download(t, f.url, {
			ALL_PROXY: f.proxy,
			NODE_EXTRA_CA_CERTS: "",
		});
		assert.equal(result.code, 1);
		assert.match(result.stderr, /self.signed certificate/i);
	});
	test(`${targetProtocol} proxy authentication failure rejects`, async (t) => {
		const f = await fixture(t, targetProtocol, "http", { reject: true });
		const result = await download(t, f.url, { ALL_PROXY: f.proxy });
		assert.equal(result.code, 1);
		assert.match(result.stderr, /407/);
		assert.equal(f.targetRequests.length, 0);
	});
	test(`${targetProtocol} stalled proxy obeys deadline and releases sockets`, async (t) => {
		const f = await fixture(t, targetProtocol, "http", { stall: true });
		const result = await download(
			t,
			f.url,
			{ ALL_PROXY: f.proxy },
			{ timeout: 100, retries: true },
		);
		assert.equal(result.code, 1);
		assert.match(result.stderr, /timed out after 100ms/);
	});
}

test("redirects choose a fresh proxy for each protocol without leaking auth", async (t) => {
	const auth = `Basic ${Buffer.from("redirect-user:secret").toString("base64")}`;
	const authenticated = (url) => {
		const proxy = new URL(url);
		proxy.username = "redirect-user";
		proxy.password = "secret";
		return proxy.href;
	};
	const last = await fixture(t, "http");
	const middle = await fixture(t, "https", "http", {
		auth,
		target: (_request, response) =>
			response.writeHead(302, { location: last.url }).end(),
	});
	const first = await fixture(t, "http", "http", {
		auth,
		target: (_request, response) =>
			response.writeHead(302, { location: middle.url }).end(),
	});
	success(
		await download(t, first.url, {
			HTTP_PROXY: authenticated(first.proxy),
			HTTPS_PROXY: authenticated(middle.proxy),
			NO_PROXY: `127.0.0.1:${last.targetPort}`,
		}),
	);
	assert.equal(first.requests.length, 1);
	assert.equal(middle.requests.length, 1);
	assert.equal(last.requests.length, 0);
	for (const f of [first, middle]) {
		assert.equal(f.requests[0].headers["proxy-authorization"], auth);
		assert.equal(f.targetRequests[0].headers["proxy-authorization"], undefined);
	}
	assert.equal(last.targetRequests[0].headers["proxy-authorization"], undefined);
});

test("HTTPS target certificate is verified through an HTTP tunnel", async (t) => {
	const f = await fixture(t, "https");
	const result = await download(t, f.url, {
		HTTPS_PROXY: f.proxy,
		NODE_EXTRA_CA_CERTS: "",
	});
	assert.equal(result.code, 1);
	assert.match(result.stderr, /self.signed certificate/i);
	assert.equal(f.targetRequests.length, 0);
});

test("proxied redirects share a deadline", async (t) => {
	const f = await fixture(t, "http", "http", {
		target: (_request, response) => {
			setTimeout(() => response.writeHead(302, { location: "/again" }).end(), 40);
		},
	});
	const result = await download(
		t,
		f.url,
		{ HTTP_PROXY: f.proxy },
		{ timeout: 100 },
	);
	assert.equal(result.code, 1);
	assert.match(result.stderr, /timed out after 100ms/);
});

test("proxy retries remove partial files within one deadline", async (t) => {
	const f = await fixture(t, "http", "http", {
		target: (_request, response) => {
			response.writeHead(200, { "content-length": body.length });
			response.write(body.subarray(0, 2));
		},
	});
	const result = await download(
		t,
		f.url,
		{ HTTP_PROXY: f.proxy },
		{ timeout: 100, retries: true },
	);
	assert.equal(result.code, 1);
	assert.match(result.stderr, /timed out after 100ms/);
	assert.equal(result.bytes, undefined);
});

test("empty lowercase proxy values defer to uppercase", {
	skip: process.platform === "win32",
}, async (t) => {
	const f = await fixture(t);
	success(
		await download(t, f.url, {
			HTTP_PROXY: f.proxy,
			http_proxy: "",
			ALL_PROXY: "http://127.0.0.1:1",
			no_proxy: "",
			NO_PROXY: "example.invalid",
		}),
	);
	assert.equal(f.requests.length, 1);
});

test("lowercase proxy variables work in an isolated environment on every OS", async (t) => {
	const f = await fixture(t);
	success(
		await download(t, f.url, {
			http_proxy: f.proxy,
			no_proxy: "example.invalid",
		}),
	);
	assert.equal(f.requests.length, 1);
});

for (const proxy of [
	"http://127.0.0.1:1",
	"http://[invalid",
	"socks5://127.0.0.1:1",
]) {
	test(`bad proxy ${proxy} fails without direct fallback`, async (t) => {
		const f = await fixture(t);
		const result = await download(t, f.url, { HTTP_PROXY: proxy });
		assert.equal(result.code, 1);
		assert.equal(f.targetRequests.length, 0);
	});
}

for (const targetProtocol of ["http", "https"]) {
	test(`${targetProtocol} stalled TLS proxy handshake is bounded`, async (t) => {
		const f = await fixture(t, targetProtocol);
		const port = await listen(
			t,
			net.createServer(() => {}),
		);
		const result = await download(
			t,
			f.url,
			{ ALL_PROXY: `https://127.0.0.1:${port}` },
			{ timeout: 100 },
		);
		assert.equal(result.code, 1);
		assert.match(result.stderr, /timed out after 100ms/);
		assert.equal(f.targetRequests.length, 0);
	});
	test(`${targetProtocol} refused proxy is bounded`, async (t) => {
		const f = await fixture(t, targetProtocol);
		const unavailable = net.createServer();
		await new Promise((resolve) => unavailable.listen(0, "127.0.0.1", resolve));
		const port = unavailable.address().port;
		await new Promise((resolve) => unavailable.close(resolve));
		const result = await download(t, f.url, {
			ALL_PROXY: `http://127.0.0.1:${port}`,
		});
		assert.equal(result.code, 1);
		// Some local network sandboxes reset rather than refuse closed ports.
		assert.match(result.stderr, /ECONNREFUSED|ECONNRESET/);
		assert.equal(f.targetRequests.length, 0);
	});
}

test("trickling CONNECT response cannot extend the deadline", async (t) => {
	const f = await fixture(t, "https");
	const proxy = net.createServer((socket) => {
		const interval = setInterval(() => socket.write("H"), 20);
		socket.on("error", () => {});
		socket.on("close", () => clearInterval(interval));
	});
	const port = await listen(t, proxy);
	const result = await download(
		t,
		f.url,
		{ HTTPS_PROXY: `http://127.0.0.1:${port}` },
		{ timeout: 100 },
	);
	assert.equal(result.code, 1);
	assert.match(result.stderr, /timed out after 100ms/);
});
