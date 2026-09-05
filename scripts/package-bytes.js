// Runs against an installed npm launcher and local release assets, never a registry release.
const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const [launcher, repo] = process.argv.slice(2);
assert.ok(launcher && repo, "usage: node package-bytes.js <installed-launcher.js> <repo>");
fs.mkdirSync(repo, { recursive: true });

function execute(command, args, expectedStatus = 0) {
	const result = spawnSync(command, args, { cwd: repo, encoding: null, env: process.env });
	assert.ifError(result.error);
	assert.equal(result.status, expectedStatus, `${command} ${args.join(" ")}: ${result.stderr}`);
	return result;
}

function runFrigo(args, expectedStatus = 0) {
	// Execute the installed JS entrypoint directly: Windows .cmd shims cannot
	// be spawned without a shell, and no shell should handle machine bytes.
	const result = execute(process.execPath, [launcher, ...args], expectedStatus);
	if (expectedStatus === 0) {
		assert.deepEqual(result.stderr, Buffer.alloc(0));
	} else {
		assert.deepEqual(result.stdout, Buffer.alloc(0));
		assert.ok(result.stderr.length > 0);
	}
	return result.stdout;
}

function splitNul(buffer) {
	assert.equal(buffer.at(-1), 0, "NUL-framed output lacks final terminator");
	const fields = [];
	let start = 0;
	while (start < buffer.length) {
		const end = buffer.indexOf(0, start);
		assert.notEqual(end, -1);
		fields.push(buffer.subarray(start, end));
		start = end + 1;
	}
	return fields;
}

execute("git", ["init", "--quiet"]);
execute("git", ["config", "user.name", "Package Test"]);
execute("git", ["config", "user.email", "package@example.invalid"]);
execute("git", ["config", "core.autocrlf", "false"]);
fs.writeFileSync(path.join(repo, "README.md"), "main\n");
execute("git", ["add", "README.md"]);
execute("git", ["commit", "--quiet", "-m", "initial"]);

const original = Buffer.from([0, 0xff, 1, 0x0a, 0]);
fs.writeFileSync(path.join(repo, "data.bin"), original);
runFrigo(["add", "data.bin"]);
assert.deepEqual(runFrigo(["list", "-z"]), Buffer.from("data.bin\0"));
assert.deepEqual(runFrigo(["ls", "-z"]), Buffer.from("data.bin\0"));
assert.deepEqual(runFrigo(["status", "--porcelain=v1", "-z"]), Buffer.from(" A data.bin\0"));
const binaryPatch = runFrigo(["diff", "--patch"]);
assert.ok(binaryPatch.includes(Buffer.from("Binary files /dev/null and b/data.bin differ\n")));
runFrigo(["commit", "-a", "-m", "binary snapshot"]);
assert.deepEqual(runFrigo(["status", "--porcelain=v1", "-z"]), Buffer.alloc(0));
assert.deepEqual(runFrigo(["diff", "--patch"]), Buffer.alloc(0));

const record = splitNul(runFrigo(["log", "--porcelain=v1", "-z", "--max-count=1"]));
assert.equal(record.length, 10);
assert.equal(record[2].toString("utf8"), "binary snapshot");
const oid = record[0].toString("ascii");
assert.deepEqual(runFrigo(["show", "--name-status", "-z", oid]), Buffer.from("A\0data.bin\0"));
assert.deepEqual(runFrigo(["show", "--patch", oid]), binaryPatch);
assert.deepEqual(runFrigo(["show", `${oid}:data.bin`]), original);
fs.writeFileSync(path.join(repo, "data.bin"), Buffer.from([0, 2, 0xff]));
assert.deepEqual(runFrigo(["status", "--porcelain=v1", "-z"]), Buffer.from(" M data.bin\0"));
runFrigo(["restore", "data.bin"]);

fs.mkdirSync(path.join(repo, "docs"));
fs.writeFileSync(path.join(repo, "docs/empty"), "");
fs.writeFileSync(path.join(repo, "docs/new"), "new\n");
runFrigo(["add", "docs"]);
const zeroOID = "0".repeat(40);
const emptyOID = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391";
const newOID = "3e757656cf36eca53338e520d134963a44f793f8";
const addedPatch = Buffer.from(
	`diff --git a/docs/empty b/docs/empty\nnew file mode 100644\nindex ${zeroOID}..${emptyOID}\n` +
	`diff --git a/docs/new b/docs/new\nnew file mode 100644\nindex ${zeroOID}..${newOID}\n` +
	"--- /dev/null\n+++ b/docs/new\n@@ -0,0 +1 @@\n+new\n",
);
assert.deepEqual(runFrigo(["diff", "--patch"]), addedPatch);
runFrigo(["commit", "-a", "-m", "text and empty files"]);
assert.deepEqual(runFrigo(["show", "HEAD:docs/empty"]), Buffer.alloc(0));
assert.deepEqual(runFrigo(["show", "--patch", "HEAD"]), addedPatch);
fs.unlinkSync(path.join(repo, "docs/new"));
assert.deepEqual(runFrigo(["status", "--porcelain=v1", "-z"]), Buffer.from(" D docs/new\0"));
const deletedPatch = Buffer.from(
	`diff --git a/docs/new b/docs/new\ndeleted file mode 100644\nindex ${newOID}..${zeroOID}\n` +
	"--- a/docs/new\n+++ /dev/null\n@@ -1 +0,0 @@\n-new\n",
);
assert.deepEqual(runFrigo(["diff", "--patch"]), deletedPatch);
runFrigo(["commit", "-a", "-m", "delete text file"]);
assert.deepEqual(runFrigo(["show", "--patch", "HEAD"]), deletedPatch);
assert.deepEqual(runFrigo(["show", "--name-status", "-z", "HEAD"]), Buffer.from("D\0docs/new\0"));
for (const command of ["status", "log"]) runFrigo([command, "--porcelain="], 2);
runFrigo(["show", "HEAD:missing"], 1);
runFrigo(["show", "--patch", "missing-revision"], 1);
runFrigo(["status", "--porcelain=v1", "-z", "--", "README.md"], 1);
console.log("installed launcher byte checks passed");
