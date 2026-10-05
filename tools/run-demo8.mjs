// Runs Demo 8 headless in Node with the demo's own code (demo/traces/app/run.js) and the same
// SQLite WebAssembly build and Go program the page uses: the whole day at full speed, one run
// reported again in the afternoon, one call rebuilt, then every check. These are the numbers the
// site publishes. With --native, the same runs also go through the native binary
// (`precomputing traces`) and the two files are compared table by table, value by value.
// Usage: node tools/run-demo8.mjs [--json] [--db FILE] [--native build/precomputing]
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, rmSync } from 'node:fs';
import { gunzipSync } from 'node:zlib';
import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { loadGo } from '../demo/lib/engine.js';
import { Run, DAY, STUCK } from '../demo/traces/app/run.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const root = new URL('..', import.meta.url);
const app = new URL('demo/traces/app/', root);
const log = (...a) => { if (!json) console.log(...a); };
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const module = await loadGo(new URL('demo/lib/go/', root).href);
const policy = readFileSync(new URL('policy.precompute', app), 'utf8');
const gz = readFileSync(new URL('data/day.jsonl.gz', app));
const meta = JSON.parse(readFileSync(new URL('data/meta.json', app), 'utf8'));
const day = gunzipSync(gz).toString('utf8');

const t0 = performance.now();
const run = await new Run(sqlite3, module, { policy, day }).init();
const initMs = performance.now() - t0;
const news = [];
const t1 = performance.now();
// The day to 15:00, one run reported again as a visitor would, then the rest of the day.
while (!run.done && run.t < DAY + 15 * 3600) run.step(Math.min(3600, DAY + 15 * 3600 - run.t), 1e9);
const again = run.reportAgain();
news.push(...run.snapshot(false).news);
const r = run.finish();
news.push(...run.news);
const wall = performance.now() - t1;
// One call rebuilt: the call of the stuck run that the page shows first.
const stuck = run.runs.find((x) => x.replies.length >= STUCK);
const rb = await run.rebuild(`${stuck.id}#${STUCK}`);
const dbPath = arg('--db');
if (dbPath) writeFileSync(dbPath, run.exportFile());

const out = {
  sqlite: r.sqlite,
  data: meta.source,
  runs: r.runs, calls: r.calls, repos: r.repos, messages: r.messages,
  wallMs: Math.round(wall), initMs: Math.round(initMs), engineMs: Math.round(r.engineMs), checkpoints: r.checkpoints,
  callsPerSec: Math.round(r.calls / (r.engineMs / 1000)),
  requestBytes: r.requestBytes, replyBytes: r.replyBytes, pieceBytes: r.pieceBytes, pieces: r.pieces,
  traceBytes: r.traceBytes, fileBytes: r.fileBytes,
  smaller: r.requestBytes / r.traceBytes,
  rebuilt: r.rebuilt, same: r.same, differ: r.differ, rebuildMs: Math.round(r.rebuildMs),
  planted: r.planted, masked: r.masked, left: r.left.length,
  checks: r.checks,
  totalNano: r.total, byCallsStream: r.byCallsStream, bySourceStream: r.bySourceStream,
  again, refused: r.refused,
  alerts: r.alerts,
  rebuild: { callId: rb.callId, bytes: rb.bytes, pieces: rb.pieces, same: rb.same, sha: rb.shaFile, shaSent: rb.shaSent, stored: rb.stored, ms: Number(rb.ms.toFixed(2)), parts: rb.parts },
  news: news.map((n) => ({ at: new Date(n.t * 1000).toISOString().slice(11, 19), kind: n.kind, text: n.text })),
  tables: r.tables,
};
const passed = r.same === r.rebuilt && r.rebuilt === r.calls && r.masked === r.planted && r.left.length === 0
  && r.checks.every((c) => c.equal === c.n && c.extra === 0) && r.byCallsStream === r.total && r.bySourceStream === r.total
  && (!again || (again.refused === again.calls && again.after === again.before)) && rb.same && rb.shaFile === rb.stored;
out.passed = passed;

// The native build on the same runs, and the two files compared.
const native = arg('--native');
if (native) {
  if (!dbPath) throw new Error('--native needs --db, the file to compare with');
  const nat = join(tmpdir(), `traces-native-${process.pid}.db`);
  rmSync(nat, { force: true });
  const put = spawnSync(native, ['traces', '--policy', new URL('examples/traces.precompute', root).pathname, ...(again ? ['--again', again.run] : []), nat], { input: gz, encoding: 'utf8' });
  if (put.status !== 0) throw new Error(`precomputing traces: ${put.stderr}`);
  const cmp = spawnSync(join(dirname(native), 'filecompare'), ['-skip', 'repo_budget_limit', nat, dbPath], { encoding: 'utf8' });
  out.native = { put: put.stdout.trim().split('\n'), compare: (cmp.stdout + cmp.stderr).trim().split('\n').slice(-1)[0], identical: cmp.status === 0 };
  for (const f of [nat, nat + '-wal', nat + '-shm']) rmSync(f, { force: true });
  out.passed = out.passed && out.native.identical;
}

if (json) {
  console.log(JSON.stringify(out, null, 1));
  process.exit(out.passed ? 0 : 1);
}
const mb = (b) => `${(b / 1e6).toFixed(2)} MB`;
for (const n of out.news) log(`  ${n.at}  ${n.text}`);
log(`\nSQLite ${out.sqlite}; ${out.runs} runs (${out.data}), ${out.calls} calls, ${out.repos} repositories in ${(wall / 1000).toFixed(1)} s; the store and the Engine took ${(out.engineMs / 1000).toFixed(2)} s (${out.callsPerSec.toLocaleString('en-US')} calls a second), ${out.checkpoints} checkpoints`);
log(`requests as sent ${mb(out.requestBytes)}; pieces ${mb(out.pieceBytes)} (${out.pieces}); trace tables ${mb(out.traceBytes)} (${out.smaller.toFixed(1)}x smaller); whole file ${mb(out.fileBytes)}`);
log(`rebuilt ${out.rebuilt} calls from the file in ${out.rebuildMs} ms: ${out.same} identical to the requests as sent${out.differ.length ? `, differ: ${out.differ.join(', ')}` : ''}`);
log(`secrets planted ${out.planted}, masked ${out.masked}, left in the file ${out.left}`);
for (const c of out.checks) log(`  ${c.name}: ${c.equal} of ${c.n} equal${c.extra ? `, ${c.extra} extra` : ''}`);
log(`total $${(out.totalNano / 1e9).toFixed(4)}; calls stream $${(out.byCallsStream / 1e9).toFixed(4)}, context stream $${(out.bySourceStream / 1e9).toFixed(4)}`);
if (again) log(`reported ${again.run} again: ${again.calls} calls, ${again.refused} refused; cost ${again.before} -> ${again.after}`, out.refused);
for (const a of out.alerts) log(`  budget: ${a.repo} past $${(a.lim / 1e9).toFixed(2)} at ${new Date(a.ts * 1000).toISOString().slice(11, 19)} (${a.callId}); ${a.after} calls after it, $${(a.afterCost / 1e9).toFixed(4)}`);
log(`rebuild ${rb.callId}: ${rb.bytes} bytes from ${rb.pieces} pieces in ${rb.ms.toFixed(1)} ms, same ${rb.same}, sha ${rb.shaFile.slice(0, 16)}… stored ${rb.stored.slice(0, 16)}…`);
for (const t of out.tables.slice(0, 12)) log(`  ${t.name.padEnd(34)} ${String(t.rows ?? '').padStart(8)} rows ${String(t.bytes).padStart(10)} bytes`);
if (out.native) log(`native: ${out.native.put.join(' | ')}\n  compare: ${out.native.compare}`);
log(out.passed ? '\nall checks passed' : '\nCHECKS FAILED');
process.exit(out.passed ? 0 : 1);
