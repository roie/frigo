#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const platform = process.platform;
const arch = process.arch;
const exe = platform === "win32" ? "frigo.exe" : "frigo";
const binary = path.join(__dirname, "..", "vendor", `${platform}-${arch}`, exe);

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
	console.error(`frigo binary not available for ${platform}-${arch}`);
	console.error(result.error.message);
	process.exit(1);
}

if (result.signal) {
	process.kill(process.pid, result.signal);
}

process.exit(result.status ?? 1);
