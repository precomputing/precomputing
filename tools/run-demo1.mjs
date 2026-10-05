// Runs Demo 1 headless in Node with the same SQLite WebAssembly build the page uses,
// and prints the numbers a run left alone ends with. These are the published numbers.
//   node tools/run-demo1.mjs [--json] [--db FILE]
//   --db saves the precomputed file as it stands at the end of the three hours.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync } from 'node:fs';
import { Run } from '../demo/sql/app/run.js';

const dir = new URL('../demo/sql/app/', import.meta.url);
const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
const run = new Run(sqlite3, {
  schema: readFileSync(new URL('policy.sql', dir), 'utf8'),
  distill: readFileSync(new URL('policy.distill.sql', dir), 'utf8'),
});
const t0 = performance.now();
const r = run.finish();
const wall = performance.now() - t0;
const dbAt = process.argv.indexOf('--db');
if (dbAt > 0) writeFileSync(process.argv[dbAt + 1], sqlite3.capi.sqlite3_js_db_export(run.pc.pointer));
if (process.argv.includes('--json')) {
  console.log(JSON.stringify({ ...r, wallMs: wall, series: run.series.length }, null, 1));
  process.exit(0);
}
const mb = (b) => (b / 1e6).toFixed(2) + ' MB';
console.log(`SQLite ${r.sqlite}; ${r.events.toLocaleString('en-US')} requests in ${(wall / 1000).toFixed(1)} s (${Math.round(r.events / (r.ingestMs / 1000)).toLocaleString('en-US')} per second through the triggers and the plain table)`);
console.log(`raw table ${mb(r.rawBytes)}; precomputed file ${mb(r.pcBytes)} (before VACUUM ${mb(r.pcBytesBeforeVacuum)}); ${r.reduction.toFixed(1)}x smaller`);
for (const x of r.rows) {
  console.log(`${x.endpoint.padEnd(14)} requests ${x.requests} = ${x.requestsExact}  avg ${x.avg.toFixed(4)} vs ${x.avgExact.toFixed(4)}  p99 ${x.p99.toFixed(2)} vs ${x.p99Exact.toFixed(2)} (${(x.p99Error * 100).toFixed(2)}%)  raw ${x.rawMs.toFixed(1)} ms`);
}
console.log(`all 15 answers read in ${r.readAllMs.toFixed(2)} ms from the precomputes vs ${r.rawAllMs.toFixed(0)} ms from the plain table; one count and average ${r.simpleReadUs.toFixed(1)} us`);
console.log(`raw tier kept in the file: ${r.rawTierRows} rows, ${mb(r.rawTierBytes)}`);
console.log('spikes', r.spikes, 'anomalies kept', r.anomalies);
console.log('outages', r.outages);
console.log('tables', r.tableRows);
