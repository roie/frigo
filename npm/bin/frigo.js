#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const { ensureBinary } = require("./install.js");

async function main() {
	const binaryPath = await ensureBinary();
	const result = spawnSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });

	if (result.error) {
		throw new Error(`frigo launcher failed to execute ${binaryPath}: ${result.error.message}`);
	}
	if (result.signal) {
		process.kill(process.pid, result.signal);
		return;
	}
	process.exit(result.status ?? 1);
}

main().catch((error) => {
	process.stderr.write(`frigo failed to prepare its binary:\n${error.message}\n`);
	process.exit(1);
});
