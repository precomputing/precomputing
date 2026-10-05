// Runs Demo 4 headless in Node with the demo's own code (demo/logs/app/run.js): the two hours of
// the web shop's logs through the log reducer and the Engine, then every check. These are the
// numbers the site publishes.
// Usage: node tools/run-demo4.mjs [--json] [--rate N] [--shard MINUTES] [--lines FILE] [--db FILE]
//   --shard breaks a search index shard that many minutes in; --lines writes every line to FILE;
//   --db saves the Engine's file.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, openSync, writeSync, closeSync } from 'node:fs';
import { loadGo } from '../demo/lib/engine.js';
import { Run } from '../demo/logs/app/run.js';
import { START } from '../demo/logs/app/scenario.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const root = new URL('..', import.meta.url);
const read = (f) => readFileSync(new URL('demo/logs/app/' + f, root), 'utf8');
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const module = await loadGo(new URL('demo/lib/go/', root).href);
const run = await new Run(sqlite3, module, { policy: read('shop.precompute'), dashboard: read('shop-dashboard.json') }).init();
if (arg('--rate')) run.setRate(Number(arg('--rate')));
const log = (...a) => { if (!json) console.log(...a); };
let fd = null;
if (arg('--lines')) {
  fd = openSync(arg('--lines'), 'w');
  const second = run.shop.second.bind(run.shop);
  run.shop.second = () => { const ls = second(); if (ls && ls.length) writeSync(fd, ls.join('\n') + '\n'); return ls; };
}
const shardAt = arg('--shard') != null ? Number(arg('--shard')) * 60 : null;
const news = [];
const t0 = performance.now();
while (!run.done) {
  const until = shardAt != null && run.t < shardAt ? shardAt - run.t : 600;
  run.step(until, 1e9);
  if (shardAt != null && run.t === shardAt) run.breakShard();
  const s = run.snapshot();
  news.push(...s.news);
}
const r = run.finish();
news.push(...run.snapshot().news);
if (fd != null) closeSync(fd);
if (arg('--db')) writeFileSync(arg('--db'), run.exportFile());
const wall = performance.now() - t0;
if (json) {
  console.log(JSON.stringify({ ...r, wallMs: wall, news }, null, 1));
  process.exit(0);
}
const clock = (t) => new Date((START + t) * 1000).toISOString().slice(11, 19);
for (const n of news.sort((a, b) => a.t - b.t)) log(`  ${clock(n.t)}  ${n.text}`);
log(`\nSQLite ${r.sqlite}; ${r.lines.toLocaleString('en-US')} lines in ${(wall / 1000).toFixed(1)} s; the reducer and Engine took ${(r.engineMs / 1000).toFixed(1)} s (${Math.round(r.lines / (r.engineMs / 1000)).toLocaleString('en-US')} lines a second)`);
log(`raw ${(r.rawBytes / 1e6).toFixed(2)} MB in ${r.lines.toLocaleString('en-US')} lines; sent ${(r.sentBytes / 1e3).toFixed(1)} KB in ${r.batches} batches, ${r.sentEvents.toLocaleString('en-US')} events: ${(r.rawBytes / r.sentBytes).toFixed(0)}x fewer bytes, ${(r.lines / r.sentEvents).toFixed(0)}x fewer events`);
log(`file ${(r.fileBytes / 1e6).toFixed(1)} MB with ${r.rawRows.toLocaleString('en-US')} lines kept; ${r.templates} templates`);
for (const [i, c] of r.checks.entries()) log(`  panel ${i}: ${c.same} of ${c.points} points match${c.worst ? `, worst ${(c.worst * 100).toFixed(2)}%` : ''}`);
for (const tp of r.tops) log('  top', JSON.stringify(tp.sent) === JSON.stringify(tp.raw) ? 'identical' : 'DIFFERENT', tp.sent.slice(0, 3));
for (const a of r.alerts) log(`  alert ${a.level} ${a.service} "${a.text}" first ${clock(a.first)}, sent ${clock(a.sent)}, ${a.caughtAfter} s`);
log(`search "${r.search.text}": ${r.search.n} lines in ${r.search.ms.toFixed(1)} ms`);
for (const t of r.tables) log(`  ${t.name.padEnd(44)} ${String(t.rows ?? '').padStart(8)} ${String(t.bytes).padStart(10)}`);
