const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');

const workflow = fs.readFileSync(path.join(__dirname, '../.github/workflows/cleanup-artifacts.yml'), 'utf8');
const lines = workflow.split(/\r?\n/);
const scripts = [];
for (let i = 0; i < lines.length; i++) {
  if (lines[i].trim() !== 'script: |') continue;
  const indent = lines[i].search(/\S/) + 2;
  const code = [];
  while (++i < lines.length) {
    if (lines[i].trim() && lines[i].search(/\S/) < indent) { i--; break; }
    code.push(lines[i].slice(indent));
  }
  scripts.push(code.join('\n'));
}
assert.equal(scripts.length, 2, 'Both actual cleanup scripts must be tested');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const runArtifacts = new AsyncFunction('context', 'github', 'core', 'process', scripts[0]);
const runCaches = new AsyncFunction('context', 'github', 'core', 'process', scripts[1]);

function fixture(input) {
  const deletedArtifacts = [];
  const deletedCaches = [];
  const failures = [];
  const now = Date.now();
  const day = 86400000;
  const summary = { addHeading() { return this; }, addTable() { return this; }, async write() {} };
  const artifacts = [
    { id: 1, created_at: new Date(now - 10 * day).toISOString(), size_in_bytes: 100 },
    { id: 2, created_at: new Date(now - day).toISOString(), size_in_bytes: 100 },
    { id: 3, created_at: new Date(now - 1000).toISOString(), size_in_bytes: 100, expired: true },
  ];
  const refs = ['refs/heads/main', 'refs/heads/live/topic', 'refs/heads/deleted',
    'refs/pull/10/merge', 'refs/pull/11/merge', 'refs/pull/12/merge', 'refs/tags/v1', 'unknown/ref'];
  const github = {
    async paginate(method) { return method(); },
    rest: {
      actions: {
        async listArtifactsForRepo() { return artifacts; },
        async deleteArtifact({ artifact_id }) { deletedArtifacts.push(artifact_id); },
        async getActionsCacheList() { return refs.map((ref, i) => ({ id: i + 1, ref, size_in_bytes: 100 })); },
        async deleteActionsCacheById({ cache_id }) { deletedCaches.push(cache_id); },
      },
      repos: { async listBranches() { return [{ name: 'main' }, { name: 'live/topic' }]; } },
      pulls: { async get({ pull_number }) {
        if (pull_number === 12) throw Object.assign(new Error('Deleted pull request'), { status: 404 });
        return { data: { state: pull_number === 10 ? 'open' : 'closed' } };
      } },
    },
  };
  const core = { summary, info() {}, warning() {}, setFailed(message) { failures.push(message); } };
  const args = [{ repo: { owner: 'fixture', repo: 'fixture' }, payload: input === undefined ? {} : { inputs: { max_age_days: input } } },
    github, core, { env: { DEFAULT_MAX_AGE_DAYS: workflow.match(/DEFAULT_MAX_AGE_DAYS: '([^']+)'/)[1] } }];
  return { args, github, failures, deletedArtifacts, deletedCaches };
}

test('Scheduled and empty-input runs use seven-day retention and delete expired artifacts', async () => {
  for (const input of [undefined, '', '  ']) {
    const f = fixture(input);
    await runArtifacts(...f.args);
    assert.deepEqual(f.deletedArtifacts, [1, 3]);
    assert.deepEqual(f.failures, []);
  }
});

test('Explicit zero deletes all artifacts; longer retention preserves unexpired artifacts', async () => {
  const all = fixture('0');
  await runArtifacts(...all.args);
  assert.deepEqual(all.deletedArtifacts, [1, 2, 3]);
  const longer = fixture('30');
  await runArtifacts(...longer.args);
  assert.deepEqual(longer.deletedArtifacts, [3]);
});

test('Invalid retention inputs cannot delete artifacts', async () => {
  for (const input of ['-1', 'invalid', 'Infinity']) {
    const f = fixture(input);
    await runArtifacts(...f.args);
    assert.deepEqual(f.deletedArtifacts, []);
    assert.equal(f.failures.length, 1);
  }
});

test('Caches for live branches, open PRs, tags and unknown refs survive', async () => {
  const f = fixture();
  await runCaches(...f.args);
  assert.deepEqual(f.deletedCaches, [3, 5, 6]);
  assert.deepEqual(f.failures, []);
});

test('A PR lookup error cannot be interpreted as a closed PR', async () => {
  const f = fixture();
  f.github.rest.pulls.get = async () => { throw Object.assign(new Error('API unavailable'), { status: 503 }); };
  await assert.rejects(runCaches(...f.args), /API unavailable/);
  assert.deepEqual(f.deletedCaches, [3]);
});
