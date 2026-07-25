#!/usr/bin/env node

const fs = require("node:fs");
const fsp = fs.promises;
const http = require("node:http");
const path = require("node:path");

function usage() {
	process.stderr.write(`usage: ${path.basename(process.argv[1])} <asset-dir> <port-file>\n`);
	process.exit(1);
}

const [assetDirectoryArg, portFileArg] = process.argv.slice(2);
if (!assetDirectoryArg || !portFileArg || process.argv.length !== 4) usage();

const assetDirectory = path.resolve(assetDirectoryArg);
const portFile = path.resolve(portFileArg);
if (!fs.existsSync(assetDirectory) || !fs.statSync(assetDirectory).isDirectory()) {
	process.stderr.write(`missing asset directory: ${assetDirectory}\n`);
	process.exit(1);
}

const server = http.createServer(async (request, response) => {
	try {
		if (request.method !== "GET") {
			response.writeHead(405).end();
			return;
		}
		const requestTarget = request.url || "/";
		const queryIndex = requestTarget.indexOf("?");
		const rawPathname = queryIndex === -1 ? requestTarget : requestTarget.slice(0, queryIndex);
		const assetName = decodeURIComponent(rawPathname).slice(1);
		if (!assetName || path.basename(assetName) !== assetName) {
			response.writeHead(404).end();
			return;
		}
		const assetPath = path.join(assetDirectory, assetName);
		const stats = await fsp.stat(assetPath);
		if (!stats.isFile()) {
			response.writeHead(404).end();
			return;
		}
		response.writeHead(200, {
			"content-length": stats.size,
			"content-type": "application/octet-stream",
		});
		const stream = fs.createReadStream(assetPath);
		stream.on("error", () => response.destroy());
		stream.pipe(response);
	} catch (error) {
		if (error.code === "ENOENT") {
			response.writeHead(404).end();
			return;
		}
		response.writeHead(500).end();
	}
});

async function writePort(port) {
	await fsp.mkdir(path.dirname(portFile), { recursive: true });
	const temporary = `${portFile}.${process.pid}.tmp`;
	await fsp.writeFile(temporary, `${port}\n`, { mode: 0o600 });
	await fsp.rename(temporary, portFile);
}

function shutdown() {
	server.close((error) => {
		if (error) {
			process.stderr.write(`${error.message}\n`);
			process.exit(1);
		}
		process.exit(0);
	});
}

server.on("error", (error) => {
	process.stderr.write(`${error.message}\n`);
	process.exit(1);
});
server.listen(0, "127.0.0.1", async () => {
	try {
		const address = server.address();
		await writePort(address.port);
	} catch (error) {
		process.stderr.write(`${error.message}\n`);
		server.close(() => process.exit(1));
	}
});
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
