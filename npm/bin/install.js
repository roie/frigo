#!/usr/bin/env node

const { createHash } = require("node:crypto");
const fs = require("node:fs");
const fsp = fs.promises;
const http = require("node:http");
const https = require("node:https");
const os = require("node:os");
const path = require("node:path");
const { Transform } = require("node:stream");
const { pipeline } = require("node:stream/promises");
const zlib = require("node:zlib");

const { HttpsProxyAgent } = require("https-proxy-agent");
const { getProxyForUrl } = require("proxy-from-env");

const INSTALL_TIMEOUT_MS = 120_000;
const MAX_REDIRECTS = 10;
const MAX_RETRIES = 3;
const RETRY_DELAY_MS = 250;
const DOWNLOAD_TIMEOUT_MS = INSTALL_TIMEOUT_MS * MAX_RETRIES;
const LOCK_POLL_MS = 200;
const LOCK_MAX_WAIT_MS = DOWNLOAD_TIMEOUT_MS + 90_000;
const HASH_PATTERN = /^[a-f0-9]{64}$/;

const TARGETS = {
	"linux-x64": { extension: "", asset: "frigo-linux-x64.gz" },
	"linux-arm64": { extension: "", asset: "frigo-linux-arm64.gz" },
	"darwin-x64": { extension: "", asset: "frigo-darwin-x64.gz" },
	"darwin-arm64": { extension: "", asset: "frigo-darwin-arm64.gz" },
	"win32-x64": { extension: ".exe", asset: "frigo-win32-x64.gz" },
	"win32-arm64": { extension: ".exe", asset: "frigo-win32-arm64.gz" },
};

function targetFor(platform = process.platform, arch = process.arch) {
	const triple = `${platform}-${arch}`;
	const target = TARGETS[triple];
	if (!target) {
		throw new Error(
			`Unsupported platform for frigo: ${triple}. Supported platforms: ${Object.keys(TARGETS).join(", ")}`,
		);
	}
	return { triple, extension: target.extension };
}

function pathApiFor(platform) {
	return platform === "win32" ? path.win32 : path.posix;
}

function defaultCacheRoot(platform = process.platform, env = process.env, homedir = os.homedir()) {
	const pathApi = pathApiFor(platform);
	if (env.FRIGO_CACHE_DIR) {
		if (platform === process.platform) {
			return path.resolve(env.FRIGO_CACHE_DIR);
		}
		return pathApi.resolve(env.FRIGO_CACHE_DIR);
	}
	if (platform === "darwin") {
		return pathApi.join(homedir, "Library", "Caches", "frigo");
	}
	if (platform === "win32") {
		const localAppData = env.LOCALAPPDATA || pathApi.join(homedir, "AppData", "Local");
		return pathApi.join(localAppData, "frigo", "Cache");
	}
	return pathApi.join(env.XDG_CACHE_HOME || pathApi.join(homedir, ".cache"), "frigo");
}

function validateEntry(entry, triple) {
	if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
		throw new Error(`Missing checksum entry for ${triple} in checksums.json`);
	}
	for (const field of ["asset", "sha256", "compressedSize", "binarySha256", "binarySize"]) {
		if (!Object.prototype.hasOwnProperty.call(entry, field)) {
			throw new Error(`Missing checksum field ${field} for ${triple}`);
		}
	}
	const expectedAsset = TARGETS[triple]?.asset;
	if (
		typeof entry.asset !== "string" ||
		entry.asset !== expectedAsset ||
		path.posix.basename(entry.asset) !== entry.asset
	) {
		throw new Error(`Invalid asset for ${triple} in checksums.json`);
	}
	for (const field of ["sha256", "binarySha256"]) {
		if (typeof entry[field] !== "string" || !HASH_PATTERN.test(entry[field])) {
			throw new Error(`Invalid checksum field ${field} for ${triple}`);
		}
	}
	for (const field of ["compressedSize", "binarySize"]) {
		if (!Number.isSafeInteger(entry[field]) || entry[field] <= 0) {
			throw new Error(`Invalid checksum field ${field} for ${triple}`);
		}
	}
}

function readJsonFile(filePath) {
	try {
		return JSON.parse(fs.readFileSync(filePath, "utf8"));
	} catch (error) {
		throw new Error(`Invalid JSON file ${filePath}: ${error.message}`);
	}
}

function readPackageVersion(packageRoot) {
	const manifest = readJsonFile(path.join(packageRoot, "package.json"));
	if (typeof manifest.version !== "string" || manifest.version.length === 0) {
		throw new Error("Could not determine frigo package version.");
	}
	return manifest.version;
}

function releaseBaseUrl(version, env = process.env) {
	const override = env.FRIGO_RELEASE_BASE_URL;
	if (override) {
		return override.replace(/\/$/, "");
	}
	return `https://github.com/roie/frigo/releases/download/v${version}`;
}

function hashFile(filePath) {
	return new Promise((resolve, reject) => {
		const hash = createHash("sha256");
		const stream = fs.createReadStream(filePath);
		stream.on("error", reject);
		stream.on("data", (chunk) => hash.update(chunk));
		stream.on("end", () => resolve(hash.digest("hex")));
	});
}

async function fileMatches(filePath, expectedSize, expectedHash) {
	try {
		const stats = await fsp.stat(filePath);
		if (!stats.isFile() || stats.size !== expectedSize) {
			return false;
		}
		return (await hashFile(filePath)) === expectedHash;
	} catch (error) {
		if (error.code === "ENOENT") return false;
		throw error;
	}
}

function byteCounter(expectedSize, label) {
	let actualSize = 0;
	const counter = new Transform({
		transform(chunk, _encoding, callback) {
			actualSize += chunk.length;
			if (actualSize > expectedSize) {
				callback(new Error(`${label} exceeds expected size of ${expectedSize} bytes`));
				return;
			}
			callback(null, chunk);
		},
	});
	return { counter, size: () => actualSize };
}

function requestWithRedirects(
	url,
	destinationPath,
	expectedSize,
	redirectsLeft = MAX_REDIRECTS,
	timeoutMs = INSTALL_TIMEOUT_MS,
	deadline = Date.now() + timeoutMs,
) {
	if (redirectsLeft < 0) {
		return Promise.reject(new Error("Too many redirects while downloading frigo binary"));
	}
	const remainingMs = deadline - Date.now();
	if (remainingMs <= 0) {
		return Promise.reject(new Error(`Download timed out after ${timeoutMs}ms`));
	}

	let parsedUrl;
	try {
		parsedUrl = new URL(url);
	} catch (error) {
		return Promise.reject(new Error(`Invalid frigo release URL: ${error.message}`));
	}
	if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:") {
		return Promise.reject(new Error(`Unsupported frigo release URL protocol: ${parsedUrl.protocol}`));
	}

	const proxy = getProxyForUrl(url);
	const requestModule = parsedUrl.protocol === "https:" ? https : http;
	const options = {
		hostname: parsedUrl.hostname,
		port: parsedUrl.port || undefined,
		path: `${parsedUrl.pathname}${parsedUrl.search}`,
		protocol: parsedUrl.protocol,
		headers: { "user-agent": "frigo-install/0.2" },
		agent: proxy ? new HttpsProxyAgent(proxy) : undefined,
	};

	return new Promise((resolve, reject) => {
		const request = requestModule.get(options, (response) => {
			const statusCode = response.statusCode || 0;
			if (statusCode >= 300 && statusCode < 400 && response.headers.location) {
				response.resume();
				const nextUrl = new URL(response.headers.location, parsedUrl).toString();
				requestWithRedirects(
					nextUrl,
					destinationPath,
					expectedSize,
					redirectsLeft - 1,
					timeoutMs,
					deadline,
				).then(resolve, reject);
				return;
			}
			if (statusCode !== 200) {
				response.resume();
				reject(new Error(`Download failed with HTTP ${statusCode} ${response.statusMessage || ""}`.trim()));
				return;
			}

			const { counter, size } = byteCounter(expectedSize, "Compressed frigo binary");
			pipeline(response, counter, fs.createWriteStream(destinationPath, { mode: 0o600 }))
				.then(() => {
					if (size() !== expectedSize) {
						reject(
							new Error(
								`Compressed frigo binary size mismatch: expected ${expectedSize} bytes, got ${size()} bytes`,
							),
						);
						return;
					}
					resolve();
				})
				.catch(reject);
		});

		const timeout = setTimeout(() => {
			request.destroy(new Error(`Download timed out after ${timeoutMs}ms`));
		}, remainingMs);
		request.on("close", () => clearTimeout(timeout));
		request.on("error", reject);
	});
}

async function downloadWithRetries(url, destinationPath, expectedSize, options = {}) {
	const attemptTimeoutMs = options.attemptTimeoutMs ?? INSTALL_TIMEOUT_MS;
	const totalTimeoutMs = options.totalTimeoutMs ?? DOWNLOAD_TIMEOUT_MS;
	const maxRetries = options.maxRetries ?? MAX_RETRIES;
	const retryDelayMs = options.retryDelayMs ?? RETRY_DELAY_MS;
	const deadline = Date.now() + totalTimeoutMs;
	let lastError;

	for (let attempt = 1; attempt <= maxRetries; attempt += 1) {
		const remainingMs = deadline - Date.now();
		if (remainingMs <= 0) {
			throw new Error(`Download timed out after ${totalTimeoutMs}ms`);
		}
		try {
			await requestWithRedirects(
				url,
				destinationPath,
				expectedSize,
				MAX_REDIRECTS,
				Math.min(attemptTimeoutMs, remainingMs),
			);
			return;
		} catch (error) {
			lastError = error;
			await fsp.rm(destinationPath, { force: true });
			if (Date.now() >= deadline) {
				throw new Error(`Download timed out after ${totalTimeoutMs}ms`);
			}
			if (attempt < maxRetries) {
				const retryRemainingMs = deadline - Date.now();
				const delayMs = Math.min(attempt * retryDelayMs, retryRemainingMs);
				await new Promise((resolve) => setTimeout(resolve, delayMs));
				if (delayMs === retryRemainingMs || Date.now() >= deadline) {
					throw new Error(`Download timed out after ${totalTimeoutMs}ms`);
				}
			}
		}
	}
	throw lastError;
}

async function decompressAndVerify(compressedPath, temporaryBinaryPath, entry) {
	const { counter, size } = byteCounter(entry.binarySize, "Decompressed frigo binary");
	try {
		await pipeline(
			fs.createReadStream(compressedPath),
			zlib.createGunzip(),
			counter,
			fs.createWriteStream(temporaryBinaryPath, {
				mode: process.platform === "win32" ? 0o644 : 0o755,
			}),
		);
	} catch (error) {
		throw new Error(`Failed to decompress frigo binary: ${error.message}`);
	}
	if (size() !== entry.binarySize) {
		throw new Error(
			`Decompressed frigo binary size mismatch: expected ${entry.binarySize} bytes, got ${size()} bytes`,
		);
	}
	const actualHash = await hashFile(temporaryBinaryPath);
	if (actualHash !== entry.binarySha256) {
		throw new Error(
			`Binary checksum mismatch: expected ${entry.binarySha256}, got ${actualHash}`,
		);
	}
}

async function releaseOwnedLock(lockPath, owner) {
	try {
		if ((await fsp.readFile(lockPath, "utf8")) === owner) {
			await fsp.rm(lockPath, { force: true });
		}
	} catch {
		// Lock cleanup is best-effort and must not mask launcher results.
	}
}

async function acquireLock(lockPath, options = {}) {
	const maxWaitMs = options.maxWaitMs ?? LOCK_MAX_WAIT_MS;
	const pollMs = options.pollMs ?? LOCK_POLL_MS;
	const startedAt = Date.now();
	while (true) {
		const owner = `${process.pid}-${Math.random().toString(16).slice(2)}\n`;
		let handle;
		try {
			handle = await fsp.open(lockPath, "wx", 0o600);
			await handle.writeFile(owner);
			await handle.close();
			handle = undefined;
			return () => releaseOwnedLock(lockPath, owner);
		} catch (error) {
			if (handle) {
				await handle.close().catch(() => {});
				await releaseOwnedLock(lockPath, owner);
			}
			if (error.code !== "EEXIST") throw error;
		}

		if (Date.now() - startedAt >= maxWaitMs) {
			throw new Error(
				`Timed out waiting for frigo binary download lock: ${lockPath}; ` +
					"verify that no Frigo installer is running, then remove it manually",
			);
		}
		await new Promise((resolve) => setTimeout(resolve, pollMs));
	}
}

function temporaryPath(directory, name, suffix) {
	const random = Math.random().toString(16).slice(2);
	return path.join(directory, `.${name}.${process.pid}.${random}.${suffix}`);
}

async function ensureBinary(options = {}) {
	const packageRoot = options.packageRoot || path.resolve(__dirname, "..");
	const platform = options.platform || process.platform;
	const arch = options.arch || process.arch;
	const env = options.env || process.env;
	const target = targetFor(platform, arch);
	const version = options.version || readPackageVersion(packageRoot);
	const manifestPath = options.manifestPath || path.join(packageRoot, "checksums.json");
	const manifest = readJsonFile(manifestPath);
	const entry = manifest[target.triple];
	validateEntry(entry, target.triple);

	const cacheRoot = options.cacheRoot || defaultCacheRoot(platform, env, options.homedir || os.homedir());
	const targetDirectory = path.join(cacheRoot, version, target.triple);
	const binaryName = `frigo${target.extension}`;
	const destination = path.join(targetDirectory, binaryName);
	await fsp.mkdir(targetDirectory, { recursive: true });

	if (await fileMatches(destination, entry.binarySize, entry.binarySha256)) {
		if (platform !== "win32") await fsp.chmod(destination, 0o755);
		return destination;
	}
	await fsp.rm(destination, { force: true });

	const lockPath = path.join(targetDirectory, `.${binaryName}.lock`);
	const releaseLock = await acquireLock(lockPath);
	try {
		if (await fileMatches(destination, entry.binarySize, entry.binarySha256)) {
			if (platform !== "win32") await fsp.chmod(destination, 0o755);
			return destination;
		}
		await fsp.rm(destination, { force: true });

		const compressedPath = temporaryPath(targetDirectory, binaryName, "download");
		const temporaryBinaryPath = temporaryPath(targetDirectory, binaryName, "tmp");
		const baseUrl = options.releaseBaseUrl || releaseBaseUrl(version, env);
		const downloadUrl = `${baseUrl}/${entry.asset}`;
		try {
			await downloadWithRetries(downloadUrl, compressedPath, entry.compressedSize);
			const compressedHash = await hashFile(compressedPath);
			if (compressedHash !== entry.sha256) {
				throw new Error(
					`Checksum mismatch for ${entry.asset}: expected ${entry.sha256}, got ${compressedHash}`,
				);
			}
			await decompressAndVerify(compressedPath, temporaryBinaryPath, entry);
			if (platform !== "win32") await fsp.chmod(temporaryBinaryPath, 0o755);
			await fsp.rename(temporaryBinaryPath, destination);
		} finally {
			await fsp.rm(compressedPath, { force: true });
			await fsp.rm(temporaryBinaryPath, { force: true });
		}
	} finally {
		await releaseLock();
	}

	return destination;
}

if (require.main === module) {
	ensureBinary()
		.then((binaryPath) => {
			process.stdout.write(`${binaryPath}\n`);
		})
		.catch((error) => {
			process.stderr.write(`frigo install failed:\n${error.message}\n`);
			process.exit(1);
		});
}

module.exports = {
	TARGETS,
	acquireLock,
	defaultCacheRoot,
	downloadWithRetries,
	ensureBinary,
	fileMatches,
	hashFile,
	releaseBaseUrl,
	requestWithRedirects,
	targetFor,
	validateEntry,
};
