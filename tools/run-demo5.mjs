// Runs Demo 5 headless in Node with the demo's own code (demo/live/app/run.js), on the simulated
// stream (demo/live/app/wiki.js), since the live one depends on the moment: minutes of changes as
// fast as they go, with some events sent twice, canary events and lines that don't read, then
// every check. The live stream goes through exactly the same code on the page.
// Usage: node tools/run-demo5.mjs [--json] [--minutes N] [--seed N] [--lines FILE] [--db FILE]
//   --lines writes every event the Engine got, as JSON lines for `precomputing put --format json`.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, openSync, writeSync, closeSync } from 'node:fs';
import { loadGo } from '../demo/lib/engine.js';
import { Run } from '../demo/live/app/run.js';
import { Simulator, mulberry32 } from '../demo/live/app/wiki.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const root = new URL('..', import.meta.url);
const minutes = Number(arg('--minutes') || 75);
const seed = Number(arg('--seed') || 5);
const START = 1790769600;               // 2026-09-30 12:00:00 UTC
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const module = await loadGo(new URL('demo/lib/go/', root).href);
const run = await new Run(sqlite3, module, readFileSync(new URL('examples/wikipedia.precompute', root), 'utf8')).init();
const log = (...a) => { if (!json) console.log(...a); };
let fd = null;
if (arg('--lines')) {
  fd = openSync(arg('--lines'), 'w');
  const names = { edits: ['wiki', 'kind', 'who'], article_edits: ['wiki', 'title'], article_editors: ['wiki', 'title', 'editor'] };
  const buf = [];
  run.onEvent = (stream, ts, key, bytes) => {
    const o = { stream, ts };
    names[stream].forEach((k, i) => { o[k] = key[i]; });
    o.bytes = bytes;
    buf.push(JSON.stringify(o));
    if (buf.length >= 5000) { writeSync(fd, buf.join('\n') + '\n'); buf.length = 0; }
  };
  run.flushLines = () => { if (buf.length) writeSync(fd, buf.join('\n') + '\n'); buf.length = 0; };
}

// The stream as a page would get it: JSON text, now and then an event twice or a line that
// doesn't read, and a little out of order across wikis.
const sim = new Simulator(seed);
const noise = mulberry32(seed + 1);
const outcome = { counted: 0, canary: 0, repeat: 0, unread: 0 };
const sent = { lines: 0, twice: 0, bad: 0 };
const checks = [];
let prev = null;
const t0 = performance.now();
for (let s = 0; s < minutes * 60; s++) {
  const ts = START + s;
  const events = sim.second(ts);
  const lines = [];
  for (const e of events) {
    const text = JSON.stringify(e);
    lines.push(text);
    if (noise() < 0.01) { lines.push(text); sent.twice++; }
  }
  if (noise() < 0.02) { lines.push('{"meta": {"domain": "en.wikipedia.org"}, "type": "edit"}'); sent.bad++; }
  if (noise() < 0.01) { lines.push('not json'); sent.bad++; }
  // A few events from the second before arrive late, as they do from busy wikis.
  if (prev) { lines.push(...prev); prev = null; }
  if (noise() < 0.2) prev = lines.splice(lines.length - 3, 3);
  for (const text of lines) {
    sent.lines++;
    let e = null;
    try { e = JSON.parse(text); } catch { outcome.unread++; continue; }
    outcome[run.add(e)]++;
  }
  if (s % 2 === 1) run.checkpoint();
  if (s % 30 === 29) checks.push(run.check(run.lastCheck ? run.lastCheck.at - 300 : null));
}
run.checkpoint();
const last = run.check();
const wall = performance.now() - t0;
if (fd != null) { run.flushLines(); closeSync(fd); }
if (arg('--db')) writeFileSync(arg('--db'), run.exportFile());
const snap = run.snapshot('wikipedias');
const all = run.snapshot('all');
const bad = checks.filter((c) => c && c.same !== c.cells);
const out = { minutes, seed, sent, outcome, counts: run.counts, engineMs: run.engineMs, wallMs: wall, checks: checks.length, badChecks: bad.length,
  last, rate: all.rate, bots: all.bots, trending: snap.trending, wikis: all.wikis, kinds: all.kinds, fileBytes: all.fileBytes, tables: run.tables(),
  readMs: all.readMs, sqlite: sqlite3.version.libVersion };
if (json) { console.log(JSON.stringify(out, null, 1)); process.exit(0); }
log(`SQLite ${out.sqlite}; ${minutes} simulated minutes, ${sent.lines.toLocaleString('en-US')} lines sent (${sent.twice} twice, ${sent.bad} that don't read) in ${(wall / 1000).toFixed(1)} s`);
log(`counted ${outcome.counted.toLocaleString('en-US')}, canary events skipped ${outcome.canary}, repeats dropped ${outcome.repeat}, unread ${outcome.unread}`);
log(`articles ${run.counts.articles.toLocaleString('en-US')}, by people ${run.counts.people.toLocaleString('en-US')}; the Engine took ${(run.engineMs / 1000).toFixed(2)} s (${Math.round(run.counts.events / (run.engineMs / 1000)).toLocaleString('en-US')} changes a second)`);
log(`checks: ${checks.length} during the run (${checks.reduce((a, c) => a + c.cells, 0).toLocaleString('en-US')} counts and sums, ${(checks.reduce((a, c) => a + c.ms, 0) / checks.length).toFixed(0)} ms each), ${bad.length} with a difference${bad.length ? `: ${bad[0].first}` : ''}`);
log(`the full check at the end: ${last.same.toLocaleString('en-US')} of ${last.cells.toLocaleString('en-US')} counts and sums equal${last.first ? `; first difference ${last.first}` : ''} (${last.ms.toFixed(0)} ms)`);
log(`last minute ${all.rate.toFixed(1)} changes a second, ${(all.bots * 100).toFixed(0)}% by bots; answers read in ${all.readMs.toFixed(1)} ms`);
log('trending on the Wikipedias:');
for (const [wiki, title, people, edits] of snap.trending) log(`  ${wiki.padEnd(20)} ${title.padEnd(24)} ${people} people, ${edits} edits`);
log('busiest wikis:', all.wikis.slice(0, 5).map(([w, n, b]) => `${w} ${n} (${Math.round((100 * b) / n)}% bots)`).join(', '));
log(`file ${(all.fileBytes / 1e6).toFixed(2)} MB`);
for (const t of out.tables) log(`  ${t.name.padEnd(36)} ${String(t.rows ?? '').padStart(8)} ${String(t.bytes).padStart(10)}`);
process.exit(0);
