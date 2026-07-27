const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const workflowDirectory = path.join(__dirname, "..", ".github", "workflows");
const workflow = fs.readFileSync(
	path.join(workflowDirectory, "release.yml"),
	"utf8",
);
const ciWorkflow = fs.readFileSync(
	path.join(workflowDirectory, "ci.yml"),
	"utf8",
);
const releaseSmokeStart = workflow.indexOf("\n  release-smoke:");
assert.notEqual(releaseSmokeStart, -1, "release-smoke job is missing");
const releaseSmoke = workflow.slice(releaseSmokeStart);
const smokeCommandStart = releaseSmoke.indexOf(
	"\n      - name: Smoke test npm install and npx",
);
assert.notEqual(smokeCommandStart, -1, "release smoke command step is missing");
const smokeCommandEnd = releaseSmoke.indexOf(
	"\n      - name: Remove global package",
	smokeCommandStart,
);
assert.notEqual(smokeCommandEnd, -1, "release smoke cleanup step is missing");
const smokeCommandStep = releaseSmoke.slice(smokeCommandStart, smokeCommandEnd);
const runMarker = "\n        run: |\n";
const smokeScriptStart = smokeCommandStep.indexOf(runMarker);
assert.notEqual(smokeScriptStart, -1, "release smoke Bash script is missing");
const smokeScript = smokeCommandStep
	.slice(smokeScriptStart + runMarker.length)
	.replace(/^ {10}/gm, "");
const retryHelperStart = smokeScript.indexOf("retry_until_deadline() {");
assert.notEqual(retryHelperStart, -1, "retry helper is missing");
const retryHelperEnd = smokeScript.indexOf("\n}\n", retryHelperStart);
assert.notEqual(retryHelperEnd, -1, "retry helper closing brace is missing");
const retryHelper = smokeScript.slice(retryHelperStart, retryHelperEnd + 2);

test("release smoke uses the exact cross-platform runner matrix", () => {
	const matrix = releaseSmoke.match(/^ {8}os: \[([^\]]+)\]$/m);
	assert.ok(matrix, "release smoke OS matrix is missing");
	assert.deepEqual(matrix[1].split(", "), [
		"ubuntu-latest",
		"macos-latest",
		"windows-latest",
	]);
	assert.doesNotMatch(releaseSmoke, /actions\/checkout/);
	assert.match(smokeCommandStep, /shell: bash/);
	assert.match(smokeCommandStep, /npm install -g/);
	assert.match(smokeCommandStep, /npx --yes/);
});

test("release smoke retries stale registry metadata until a shared deadline", () => {
	assert.match(
		smokeCommandStep,
		/NPM_CONFIG_CACHE: \$\{\{ runner\.temp \}\}\/npm-smoke-cache/,
	);
	assert.match(smokeCommandStep, /NPM_CONFIG_PREFER_ONLINE: "true"/);
	assert.match(smokeCommandStep, /timeout-minutes: 5/);
	assert.match(smokeCommandStep, /deadline=\$\(\(SECONDS \+ 300\)\)/);
	assert.match(
		smokeCommandStep,
		/retry_until_deadline "npm install" npm install -g "frigo@\$\{version\}"/,
	);
	assert.match(smokeCommandStep, /retry_until_deadline "npx" check_npx/);
	assert.doesNotMatch(smokeCommandStep, /for attempt in 1 2 3 4 5/);
});

test("embedded retry helper rejects expired work and clips retry sleep", () => {
	const behaviorScript = [
		retryHelper,
		"SECONDS=10",
		"deadline=10",
		"started=0",
		"already_expired_operation() { started=1; return 0; }",
		'if retry_until_deadline "already expired" already_expired_operation; then exit 91; fi',
		'test "$started" -eq 0',
		'printf "already-expired-rejected\\n"',
		"SECONDS=0",
		"deadline=1",
		"started=0",
		"late_operation() { started=1; SECONDS=$((deadline + 1)); return 0; }",
		'if retry_until_deadline "late success" late_operation; then exit 92; fi',
		'test "$started" -eq 1',
		'printf "late-success-rejected\\n"',
		"SECONDS=0",
		"deadline=3",
		"attempts=0",
		"always_fails() { attempts=$((attempts + 1)); return 1; }",
		'sleep() { printf "sleep:%s\\n" "$1"; SECONDS=$((SECONDS + $1)); }',
		'if retry_until_deadline "sleep clipping" always_fails; then exit 93; fi',
		'test "$attempts" -eq 1',
		'printf "sleep-clipped\\n"',
	].join("\n");
	const result = spawnSync("bash", ["-c", behaviorScript], {
		encoding: "utf8",
		timeout: 5_000,
	});

	assert.ifError(result.error);
	assert.equal(
		result.status,
		0,
		`embedded retry helper failed:\n${result.stdout}${result.stderr}`,
	);
	assert.match(result.stdout, /already-expired-rejected/);
	assert.match(result.stdout, /late-success-rejected/);
	assert.match(result.stdout, /sleep:3/);
	assert.match(result.stdout, /sleep-clipped/);
});

test("release publication is draft-first and resumable", () => {
	assert.match(workflow, /gh release view/);
	assert.match(workflow, /isDraft/);
	assert.match(workflow, /isImmutable/);
	assert.match(workflow, /gh release create[^\n]*--draft/);
	assert.match(workflow, /gh release upload[^\n]*--clobber/);
	assert.match(workflow, /gh release edit[^\n]*--draft=false/);
	assert.match(workflow, /npm view "frigo@\$\{version\}" version/);
	assert.match(
		workflow,
		/npm publish "\.\/npm-dist\/\$TARBALL" --access public --provenance/,
	);
});

test("all local package gates run on the publish tarball before GitHub release mutation", () => {
	const pack = workflow.indexOf("- name: Pack npm package");
	const smoke = workflow.indexOf("- name: Smoke test packed npm package");
	const releaseMutation = workflow.indexOf("- name: Prepare GitHub release");
	assert.ok(pack >= 0, "pack step is missing");
	assert.ok(smoke > pack, "packed package smoke must follow npm pack");
	assert.ok(
		releaseMutation > smoke,
		"GitHub release mutation must follow local smoke tests",
	);
	const smokeStep = workflow.slice(smoke, releaseMutation);
	assert.match(smokeStep, /TARBALL: \$\{\{ steps\.pack\.outputs\.tarball \}\}/);
	assert.match(
		smokeStep,
		/package_test\.sh "\$\{VERSION#v\}" "\$PWD\/npm-dist\/\$TARBALL" "\$PWD\/release-assets"/,
	);
});

test("manual recovery reuses a public release without mutating it", () => {
	assert.match(workflow, /workflow_dispatch:[\s\S]*release_tag:/);
	const checkoutCount = (workflow.match(/uses: actions\/checkout@/g) || [])
		.length;
	const resolvedCheckoutCount = (
		workflow.match(/ref: \$\{\{ inputs\.release_tag \|\| github\.ref \}\}/g) ||
		[]
	).length;
	assert.equal(resolvedCheckoutCount, checkoutCount);
	assert.match(workflow, /Download existing public release assets/);
	assert.match(workflow, /Verify existing public release assets/);
	assert.match(workflow, /gh release view "\$RELEASE_TAG" --json isDraft/);
	assert.match(
		workflow,
		/gh release download "\$RELEASE_TAG" --dir "\$PWD\/release-assets" --clobber/,
	);
	assert.match(
		workflow,
		/VERSION: \$\{\{ inputs\.release_tag \|\| github\.ref_name \}\}/,
	);

	const download = workflow.indexOf(
		"- name: Download existing public release assets",
	);
	const verify = workflow.indexOf(
		"- name: Verify existing public release assets",
	);
	const packageGeneration = workflow.indexOf(
		"- name: Generate npm package files",
	);
	assert.ok(download >= 0 && verify > download && packageGeneration > verify);

	for (const name of [
		"Prepare GitHub release",
		"Upload draft release assets",
		"Publish GitHub release",
	]) {
		const start = workflow.indexOf(`- name: ${name}`);
		const end = workflow.indexOf("\n      - name:", start + 1);
		const step = workflow.slice(start, end === -1 ? undefined : end);
		assert.match(step, /if: github\.event_name == 'push'/, name);
	}
});

test("workflow avoids shell interpolation hazards", () => {
	assert.doesNotMatch(workflow, /node -e "/);
	assert.doesNotMatch(workflow, /verify-release-assets\.js "\$\{\{/);
	assert.doesNotMatch(workflow, /run:[^\n]*\$\{\{ github\.ref_name \}\}/);
});

test("CI uses least privilege and non-persisted checkout credentials", () => {
	assert.match(ciWorkflow, /permissions:\n {2}contents: read/);
	const checkoutCount = (ciWorkflow.match(/uses: actions\/checkout@/g) || [])
		.length;
	const nonPersistentCount = (
		ciWorkflow.match(/persist-credentials: false/g) || []
	).length;
	assert.equal(checkoutCount, 2);
	assert.equal(nonPersistentCount, checkoutCount);
});

test("CI runs release safety tests directly", () => {
	assert.match(
		ciWorkflow,
		/node --test \\\n\s+scripts\/release-assets\.test\.js \\\n\s+scripts\/release-workflow\.test\.js \\\n\s+scripts\/verify-release-assets\.test\.js/,
	);
});

test("release compatibility tests show on Git 2.23", () => {
	const start = workflow.indexOf("- name: Run Git 2.23 compatibility tests");
	assert.notEqual(start, -1, "Git 2.23 compatibility step is missing");
	const end = workflow.indexOf("\n  release:", start);
	const compatibilityStep = workflow.slice(start, end === -1 ? undefined : end);
	assert.match(compatibilityStep, /\^TestShowReportsLatestCommitAndFullPatch\$/);
});
