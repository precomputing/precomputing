// Runs Demo 3 headless in Node with the demo's own code (demo/meter/app/run.js) and the same
// SQLite WebAssembly build the page uses: the whole month at full speed, then every check.
// These are the numbers the site publishes.
// Usage: node tools/run-demo3.mjs [--json] [--storm HOURS] [--cut HOURS] [--csv FILE] [--db FILE]
//   --storm and --cut start a retry storm or cut the eu link at that many hours into the month.
//   --csv writes every report in the order the meter received it, as `seq,ts,request_id,customer,
//   model,gateway,input_tokens,output_tokens` lines for `precomputing put --seq`; --db saves the file.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, openSync, writeSync, closeSync } from 'node:fs';
import { MODELS, CUSTOMERS } from '../demo/meter/app/scenario.js';
import { Run } from '../demo/meter/app/run.js';
import { START } from '../demo/meter/app/scenario.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? Number(process.argv[i + 1]) : null; };
const dir = new URL('../demo/meter/app/', import.meta.url);
const read = (f) => readFileSync(new URL(f, dir), 'utf8');
const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
const run = new Run(sqlite3, { schema: read('policy.sql'), distill: read('policy.distill.sql'), billing: read('billing.sql') });
const log = (...a) => { if (!json) console.log(...a); };
const csvPath = process.argv.includes('--csv') ? process.argv[process.argv.indexOf('--csv') + 1] : null;
if (csvPath) {
  const fd = openSync(csvPath, 'w');
  let seq = 0, buf = [];
  const flush = () => { if (buf.length) { writeSync(fd, buf.join('')); buf = []; } };
  const deliver = run.deliver.bind(run);
  run.deliver = (it, extra) => {
    buf.push(`${++seq},${it.ts},${it.id},${CUSTOMERS[it.c].id},${MODELS[it.m]},${it.gw},${it.input},${it.output}\n`);
    if (buf.length >= 10000) flush();
    return deliver(it, extra);
  };
  process.on('exit', () => { flush(); closeSync(fd); });
}
const actions = [];
if (arg('--storm') != null) actions.push({ at: arg('--storm') * 3600, go: () => run.retryStorm() });
if (arg('--cut') != null) actions.push({ at: arg('--cut') * 3600, go: () => run.cutLink('eu') });
const t0 = performance.now();
const news = [];
while (!run.done) {
  const next = actions.find((a) => !a.done);
  run.step(next ? Math.max(60, next.at - (run.t - START)) : 86400, 1e9);
  if (next && run.t - START >= next.at) { next.go(); next.done = true; }
  news.push(...run.news);
  run.news = [];
}
const r = run.finish();
news.push(...run.news);
const dbPath = process.argv.includes('--db') ? process.argv[process.argv.indexOf('--db') + 1] : null;
if (dbPath) writeFileSync(dbPath, run.exportFile());
const wall = performance.now() - t0;
const usd = (nano) => '$' + (Number(BigInt(nano) + 5000000n) / 1e9 - 0.005).toFixed(2);
if (json) {
  console.log(JSON.stringify({ ...r, wallMs: wall, news }, (k, v) => (typeof v === 'bigint' ? v.toString() : v), 1));
  process.exit(0);
}
for (const n of news) log(`  ${new Date((START + n.t) * 1000).toISOString().slice(5, 16).replace('T', ' ')}  ${n.text}`);
log(`\nSQLite ${r.sqlite}; ${r.served.toLocaleString('en-US')} requests served, ${r.reports.toLocaleString('en-US')} reports in ${(wall / 1000).toFixed(1)} s (${Math.round(r.reports / (r.ingestMs / 1000)).toLocaleString('en-US')} reports a second through the triggers)`);
log(`September: ${r.servedSept.toLocaleString('en-US')} served, ${r.billed.toLocaleString('en-US')} billed (meter ${r.meterSept}), raw rows ${r.rawRows} distinct ${r.rawDistinct}`);
log(`invoice lines ${r.lines}: recount diffs ${r.recountDiffs}, raw diffs ${r.rawDiffs}; amounts diffs ${r.amountDiffs}; hourly windows ${r.hourRows} diffs ${r.hourDiffs}; refused rows ${r.refusedRows} diffs ${r.refusedDiffs}`, r.refused);
log(`stuck`, r.stuck, 'after midnight', r.afterMidnight);
for (const i of r.invoices) log(`  ${i.name.padEnd(22)} ${i.plan.padEnd(10)} ${String(i.requests).padStart(7)} req  in ${String(i.input_tokens).padStart(11)}  out ${String(i.output_tokens).padStart(11)}  list ${usd(i.list_nano).padStart(10)}  due ${usd(i.due_nano).padStart(10)}  cost ${usd(i.cost_nano).padStart(10)}  ${i.ok ? 'ok' : 'DIFF'}`);
log(`total due ${usd(r.due)}, cost ${usd(r.cost)}, margin ${(100 * (1 - Number(r.cost) / Number(r.due))).toFixed(1)}%`);
log('turned away', r.away);
log(`invoices read in ${r.invoiceMs.toFixed(3)} ms from the precomputes, ${r.rawInvoiceMs.toFixed(0)} ms from every request (same: ${r.slowOk}); a quota check ${r.quotaUs.toFixed(2)} us`);
log(`file ${(r.fileBytes / 1e6).toFixed(2)} MB; after the dispute window ${(r.laterBytes / 1e6).toFixed(2)} MB; invoices the same: ${r.laterSame}; September raw left ${r.laterRaw}, October raw ${r.laterOct}`);
for (const t of r.tablesNow) log(`  ${t.name.padEnd(40)} ${String(t.rows ?? '').padStart(8)} rows ${String(t.bytes).padStart(10)} bytes`);
log('after:');
for (const t of r.tablesLater) log(`  ${t.name.padEnd(40)} ${String(t.rows ?? '').padStart(8)} rows ${String(t.bytes).padStart(10)} bytes`);
