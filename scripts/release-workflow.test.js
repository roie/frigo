const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const workflowDirectory = path.join(__dirname, "..", ".github", "workflows");
const workflow = fs.readFileSync(path.join(workflowDirectory, "release.yml"), "utf8");
const ciWorkflow = fs.readFileSync(path.join(workflowDirectory, "ci.yml"), "utf8");
const releaseSmokeStart = workflow.indexOf("\n  release-smoke:");
assert.notEqual(releaseSmokeStart, -1, "release-smoke job is missing");
const releaseSmoke = workflow.slice(releaseSmokeStart);

test("release smoke is cross-platform and does not persist checkout credentials", () => {
	assert.doesNotMatch(releaseSmoke, /actions\/checkout/);
	assert.match(releaseSmoke, /shell: bash/);
	assert.match(releaseSmoke, /npm install -g/);
	assert.match(releaseSmoke, /npx --yes/);
});

test("release publication is draft-first and resumable", () => {
	assert.match(workflow, /gh release view/);
	assert.match(workflow, /isDraft/);
	assert.match(workflow, /isImmutable/);
	assert.match(workflow, /gh release create[^\n]*--draft/);
	assert.match(workflow, /gh release upload[^\n]*--clobber/);
	assert.match(workflow, /gh release edit[^\n]*--draft=false/);
	assert.match(workflow, /npm view "frigo@\$\{version\}" version/);
	assert.match(workflow, /npm publish "\.\/npm-dist\/\$TARBALL" --access public --provenance/);
});

test("all local package gates run on the publish tarball before GitHub release mutation", () => {
	const pack = workflow.indexOf("- name: Pack npm package");
	const smoke = workflow.indexOf("- name: Smoke test packed npm package");
	const releaseMutation = workflow.indexOf("- name: Prepare GitHub release");
	assert.ok(pack >= 0, "pack step is missing");
	assert.ok(smoke > pack, "packed package smoke must follow npm pack");
	assert.ok(releaseMutation > smoke, "GitHub release mutation must follow local smoke tests");
	const smokeStep = workflow.slice(smoke, releaseMutation);
	assert.match(smokeStep, /TARBALL: \$\{\{ steps\.pack\.outputs\.tarball \}\}/);
	assert.match(smokeStep, /package_test\.sh "\$\{VERSION#v\}" "\$PWD\/npm-dist\/\$TARBALL" "\$PWD\/release-assets"/);
});

test("workflow avoids shell interpolation hazards", () => {
	assert.doesNotMatch(workflow, /node -e "/);
	assert.doesNotMatch(workflow, /verify-release-assets\.js "\$\{\{/);
	assert.doesNotMatch(workflow, /run:[^\n]*\$\{\{ github\.ref_name \}\}/);
});

test("CI uses least privilege and non-persisted checkout credentials", () => {
	assert.match(ciWorkflow, /permissions:\n {2}contents: read/);
	const checkoutCount = (ciWorkflow.match(/uses: actions\/checkout@/g) || []).length;
	const nonPersistentCount = (ciWorkflow.match(/persist-credentials: false/g) || []).length;
	assert.equal(checkoutCount, 2);
	assert.equal(nonPersistentCount, checkoutCount);
});
