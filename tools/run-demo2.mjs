// Runs Demo 2 headless in Node with the demo's own code (demo/engine/app/run.js): the whole
// trading day at full speed, with the scripted pull of the plug, then the race against the
// compiled triggers. These are the numbers the site publishes.
// Usage: node tools/run-demo2.mjs [--json] [--no-race] [--db FILE]
//   --db saves the Engine's file as it stands at the end of the day.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadGo } from '../demo/lib/engine.js';
import { Run, Race } from '../demo/engine/app/run.js';

const json = process.argv.includes('--json');
const root = new URL('..', import.meta.url);
const policy = readFileSync(new URL('demo/engine/app/policy.precompute', root), 'utf8');
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const module = await loadGo(new URL('demo/lib/go/', root).href);
const log = (...a) => { if (!json) console.log(...a); };

const run = await new Run(sqlite3, module, policy).init();
const t0 = performance.now();
let shown = 0;
while (!run.done) {
  await run.step(1e6, 250);
  if (run.t - shown >= 3600 || run.done) {
    shown = run.t;
    log(`  ${String(Math.floor((34200 + run.t) / 3600)).padStart(2, '0')}:${String(Math.floor((34200 + run.t) % 3600 / 60)).padStart(2, '0')} New York  ${run.trades.toLocaleString('en-US')} trades`);
  }
}
const wall = performance.now() - t0;
const r = run.finish();
const dbAt = process.argv.indexOf('--db');
if (dbAt > 0) writeFileSync(process.argv[dbAt + 1], run.exportFile());
const out = {
  sqlite: sqlite3.version.libVersion,
  trades: r.trades,
  wallMs: Math.round(wall),
  engineMs: Math.round(r.engineMs),
  engineTradesPerSec: Math.round(r.trades / (r.engineMs / 1000)),
  checkpoints: r.checkpoints,
  checkpointMs: Math.round(r.checkpointMs),
  fileBytes: r.fileBytes,
  plainBytes: r.plainBytes,
  reduction: r.plainBytes / r.fileBytes,
  candlesChecked: r.check.checked,
  candleDiffs: r.check.diffs,
  byRes: r.check.byRes,
  quoteDiffs: r.quoteDiffs,
  tradesTotal: r.total,
  sourceSeq: r.seq,
  sourceEvents: r.events,
  rawRows: r.rawRows,
  rawDiffs: r.rawDiffs,
  alerts: r.alerts.map((a) => ({ ts: a.ts, symbol: a.symbol, price: a.price, before: a.before, z: Math.round(a.z) })),
  plugs: r.plugs.map((p) => ({ by: p.by, t: p.t, lost: p.lost, cutRows: p.cutRows, resent: p.resent, openMs: Math.round(p.openMs), recoverMs: Math.round(p.recoverMs), checked: p.check?.checked, diffs: p.check?.diffs })),
  tables: r.tables,
};
log(`\n${out.trades.toLocaleString('en-US')} trades; the Engine spent ${(out.engineMs / 1000).toFixed(1)} s on them (${out.engineTradesPerSec.toLocaleString('en-US')} trades a second), ${out.checkpoints} checkpoints taking ${(out.checkpointMs / 1000).toFixed(1)} s`);
log(`file ${(out.fileBytes / 1e6).toFixed(1)} MB; a plain table of every trade would take about ${(out.plainBytes / 1e6).toFixed(0)} MB (${out.reduction.toFixed(0)}x)`);
log(`recount: ${out.candlesChecked} candles checked, ${out.candleDiffs} differences`, out.byRes);
log(`quote board differences ${out.quoteDiffs}; trades_total ${out.tradesTotal}; sources seq ${out.sourceSeq} events ${out.sourceEvents}; raw tier ${out.rawRows} rows, ${out.rawDiffs} differences`);
for (const a of out.alerts) log(`  alert ${new Date(a.ts * 1000).toISOString().slice(11, 19)} UTC ${a.symbol} ${a.before} -> ${a.price} z=${a.z}`);
for (const p of out.plugs) log(`  plug (${p.by}) at t=${p.t}: ${p.lost} trades only in memory, checkpoint cut after ${p.cutRows} rows, ${p.resent} resent, back in ${p.recoverMs} ms (open ${p.openMs} ms); after: ${p.checked} candles checked, ${p.diffs} differences`);
for (const t of out.tables) log(`  ${t.name.padEnd(30)} ${String(t.rows ?? '').padStart(8)} rows ${String(t.bytes).padStart(10)} bytes`);

if (!process.argv.includes('--no-race')) {
  const race = new Race(sqlite3, run.go, policy, run.compiled);
  while (!race.done) race.step(25);
  const p = race.progress();
  const cmp = race.compare();
  race.close();
  out.race = {
    trades: p.total,
    engineMs: Math.round(p.engine.ms), sqlMs: Math.round(p.sql.ms),
    engineRate: Math.round(p.total / (p.engine.ms / 1000)), sqlRate: Math.round(p.total / (p.sql.ms / 1000)),
    speedup: p.sql.ms / p.engine.ms,
    tables: cmp,
    identical: cmp.every((t) => t.diffs === 0),
    rows: cmp.reduce((a, t) => a + t.rows, 0),
    values: cmp.reduce((a, t) => a + t.values, 0),
  };
  log(`\nrace, first ${p.total.toLocaleString('en-US')} trades: Engine ${out.race.engineRate.toLocaleString('en-US')}/s, triggers ${out.race.sqlRate.toLocaleString('en-US')}/s, ${out.race.speedup.toFixed(1)}x; ${out.race.rows} rows (${out.race.values} values) ${out.race.identical ? 'identical' : 'DIFFER'}`);
  for (const t of cmp) log(`  ${t.name.padEnd(30)} ${String(t.rows).padStart(7)} rows ${t.diffs ? t.diffs + ' differ' : 'identical'}`);
}
if (json) console.log(JSON.stringify(out, null, 2));
process.exit(0);
