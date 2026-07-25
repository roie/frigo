#!/usr/bin/env node

const { createHash } = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const { verifyManifest } = require("./build-release-assets.js");

const REQUEST_TIMEOUT_MS = 120_000;

function usage() {
	const scriptName = path.basename(process.argv[1]);
	process.stderr.write(`usage: ${scriptName} <release-tag> <repo> <checksums-json>\n`);
	process.exit(1);
}

function sha256(buffer) {
	return createHash("sha256").update(buffer).digest("hex");
}

async function fetchBuffer(url, fetchImpl, timeoutMs) {
	const controller = new AbortController();
	const timeout = setTimeout(() => controller.abort(), timeoutMs);
	try {
		const response = await fetchImpl(url, { redirect: "follow", signal: controller.signal });
		if (!response.ok) {
			throw new Error(`${response.status} ${response.statusText}`.trim());
		}
		return Buffer.from(await response.arrayBuffer());
	} catch (error) {
		if (error.name === "AbortError") {
			throw new Error(`request timed out after ${timeoutMs}ms (${url})`);
		}
		throw error;
	} finally {
		clearTimeout(timeout);
	}
}

async function verifyRelease({
	baseUrl,
	manifestPath,
	fetchImpl = globalThis.fetch,
	timeoutMs = REQUEST_TIMEOUT_MS,
}) {
	if (typeof fetchImpl !== "function") {
		throw new Error("Node.js fetch API is unavailable");
	}
	if (!baseUrl || !manifestPath) {
		throw new Error("baseUrl and manifestPath are required");
	}
	if (!fs.existsSync(manifestPath)) {
		throw new Error(`missing checksums file: ${manifestPath}`);
	}

	const localManifestBytes = fs.readFileSync(manifestPath);
	let manifest;
	try {
		manifest = JSON.parse(localManifestBytes.toString("utf8"));
	} catch (error) {
		throw new Error(`invalid local checksums.json: ${error.message}`);
	}
	verifyManifest(manifest);

	const normalizedBaseUrl = baseUrl.replace(/\/$/, "");
	let remoteManifestBytes;
	try {
		remoteManifestBytes = await fetchBuffer(
			`${normalizedBaseUrl}/checksums.json`,
			fetchImpl,
			timeoutMs,
		);
	} catch (error) {
		throw new Error(`checksums.json: ${error.message}`);
	}
	if (!remoteManifestBytes.equals(localManifestBytes)) {
		throw new Error("remote checksums.json does not match the local release manifest");
	}

	let verified = 0;
	for (const [triple, entry] of Object.entries(manifest)) {
		const url = `${normalizedBaseUrl}/${encodeURIComponent(entry.asset)}`;
		let body;
		try {
			body = await fetchBuffer(url, fetchImpl, timeoutMs);
		} catch (error) {
			throw new Error(`${triple}: ${error.message} (${url})`);
		}
		if (body.length !== entry.compressedSize) {
			throw new Error(
				`${triple}: compressedSize=${body.length}/${entry.compressedSize} (${url})`,
			);
		}
		const actualHash = sha256(body);
		if (actualHash !== entry.sha256) {
			throw new Error(`${triple}: sha256=${actualHash}/${entry.sha256} (${url})`);
		}
		verified += 1;
	}

	return verified;
}

async function main() {
	const [releaseTag, repository, checksumsPath] = process.argv.slice(2);
	if (!releaseTag || !repository || !checksumsPath || process.argv.length !== 5) {
		usage();
	}
	if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) {
		throw new Error(`invalid repository: ${repository}`);
	}
	const baseUrl = `https://github.com/${repository}/releases/download/${encodeURIComponent(releaseTag)}`;
	const count = await verifyRelease({ baseUrl, manifestPath: checksumsPath });
	process.stdout.write(`verified ${count} release assets from ${baseUrl}\n`);
}

if (require.main === module) {
	main().catch((error) => {
		process.stderr.write(`${error.message || String(error)}\n`);
		process.exit(1);
	});
}

module.exports = { fetchBuffer, verifyRelease };
