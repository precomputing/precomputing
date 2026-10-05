// Runs Demo 6 headless in Node with the demo's own code (demo/dejavu/app/run.js): two hours of the
// web shop's logs today, on the file that holds the last 24 hours, checked every ten seconds against
// the incidents written down, then every check. These are the numbers the site publishes.
// Usage: node tools/run-demo6.mjs [--json] [--history] [--trials N] [--lines FILE] [--db FILE]
//   --history  rebuilds the history file the way tools/build-dejavu-history.mjs does and checks it:
//              the same bytes as the file the page loads, every count against a recount, and every
//              minute of the day checked for false alarms.
//   --trials   runs N more days with other seeds and incidents started at random, none scripted.
//   --lines    writes every line, the history's and then today's, to FILE (for the native Engine).
//   --db       saves the file as it is at the end of the day.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, openSync, writeSync, closeSync } from 'node:fs';
import { gunzipSync } from 'node:zlib';
import { createHash } from 'node:crypto';
import { loadGo, startGo } from '../demo/lib/engine.js';
import { Run, kindKey, GRACE } from '../demo/dejavu/app/run.js';
import { buildHistory, HISTORY_CHECKPOINT } from '../demo/dejavu/app/history.js';
import { TODAY_START, TODAY_DURATION, HISTORY_START, HISTORY_INCIDENTS, historyShop, mulberry32, KINDS } from '../demo/dejavu/app/scenario.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const root = new URL('..', import.meta.url);
const read = (f) => readFileSync(new URL(f, root), 'utf8');
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const module = await loadGo(new URL('demo/lib/go/', root).href);
const shipped = new Uint8Array(gunzipSync(readFileSync(new URL('demo/dejavu/app/history.db.gz', root))));
const sha = (b) => createHash('sha256').update(b).digest('hex');
const log = (...a) => { if (!json) console.log(...a); };
const clock = (ts) => new Date(ts * 1000).toISOString().slice(11, 19);
const out = { sqlite: sqlite3.version.libVersion };

let fd = null;
if (arg('--lines')) fd = openSync(arg('--lines'), 'w');

// The history, built again: its bytes, its counts and every minute of it checked.
if (process.argv.includes('--history')) {
  const counts = new Map();
  let ts = HISTORY_START;
  const alarms = [];
  const t0 = performance.now();
  const go = await startGo(module);
  const h = buildHistory(sqlite3, go, read('examples/dejavu.precompute'), read('demo/dejavu/app/dejavu.sql'), {
    onLines(lines) {
      const m = ts - (ts % 60);
      for (const l of lines) {
        const x = /^\S+\s+(\S+)\s+(\S+)\s+/.exec(l);
        const k = `${m} ${x[2]} ${x[1]}`;
        counts.set(k, (counts.get(k) || 0) + 1);
      }
      if (fd != null) writeSync(fd, lines.join('\n') + '\n');
      ts++;
    },
    onCheck(db, at) {
      // Lines were counted a second at a time; a second without lines still moves the clock.
      ts = at;
      if (at < HISTORY_START + 3600 + 120) return;       // the first hour has no hour before it
      const fp = db.selectArrays('SELECT service, level, template, round(now_rate, 1), round(usual_rate, 1) FROM fingerprint_now');
      if (fp.length) alarms.push({ at, fp });
    },
  });
  const buildMs = performance.now() - t0;
  // The same file as the one shipped, byte for byte?
  const same = sha(h.bytes) === sha(shipped);
  // Every minute's count by service and level against the recount.
  const db = new sqlite3.oo1.DB(':memory:');
  const p = sqlite3.wasm.allocFromTypedArray(h.bytes);
  db.checkRc(sqlite3.capi.sqlite3_deserialize(db.pointer, 'main', p, h.bytes.byteLength, h.bytes.byteLength,
    sqlite3.capi.SQLITE_DESERIALIZE_FREEONCLOSE | sqlite3.capi.SQLITE_DESERIALIZE_RESIZEABLE));
  const got = new Map(db.selectArrays('SELECT w, service, level, sum(n) FROM lines_win WHERE res = 60 GROUP BY w, service, level')
    .map(([w, s, l, n]) => [`${w} ${s} ${l}`, n]));
  let cells = 0, equal = 0;
  for (const k of new Set([...counts.keys(), ...got.keys()])) { cells++; if (counts.get(k) === got.get(k)) equal++; }
  db.close();
  // Minutes where something stood out, against the incidents written down.
  const spans = HISTORY_INCIDENTS.map((x, i) => ({ id: i + 1, from: x.at, to: x.at + x.minutes * 60 + GRACE + HISTORY_CHECKPOINT }));
  const inIncident = alarms.filter((a) => spans.some((s) => a.at >= s.from && a.at <= s.to));
  const outside = alarms.filter((a) => !inIncident.includes(a));
  const caught = spans.map((s) => { const a = alarms.find((x) => x.at >= s.from && x.at <= s.to); return { id: s.id, after: a ? a.at - s.from : null }; });
  const minutesChecked = (HISTORY_START + 86400 - (HISTORY_START + 3600 + 120)) / 60 + 1;
  out.history = { lines: h.lines, rawBytes: h.rawBytes, templates: h.templates, bytes: h.bytes.byteLength, sha256: sha(h.bytes).slice(0, 16),
    sameAsShipped: same, buildMs, counts: { cells, equal }, minutesChecked: Math.floor(minutesChecked), alarms: alarms.length, inIncident: inIncident.length,
    falseAlarms: outside.map((a) => ({ at: a.at, fp: a.fp })), caught };
  log(`history: ${h.lines.toLocaleString('en-US')} lines built again in ${(buildMs / 1000).toFixed(1)} s; ${same ? 'the same bytes as' : 'DIFFERENT from'} the file the page loads (sha256 ${sha(h.bytes).slice(0, 16)})`);
  log(`  counts by minute, service and level: ${equal.toLocaleString('en-US')} of ${cells.toLocaleString('en-US')} equal to a recount`);
  log(`  ${Math.floor(minutesChecked)} minutes checked: something stood out in ${alarms.length}, ${inIncident.length} of them during the six incidents, ${outside.length} outside them`);
  for (const a of outside) log(`    false alarm at ${clock(a.at)}: ${a.fp.map((r) => r.join('/')).join('; ')}`);
  for (const c of caught) log(`  incident #${c.id}: ${c.after == null ? 'NOT CAUGHT' : `stood out at the first check, ${c.after} s after it began`}`);
} else if (fd != null) {
  const shop = historyShop();
  for (let ls = shop.second(); ls; ls = shop.second()) if (ls.length) writeSync(fd, ls.join('\n') + '\n');
}

// Today, as scripted.
const run = await new Run(sqlite3, module, shipped).init();
if (fd != null) {
  const second = run.shop.second.bind(run.shop);
  run.shop.second = () => { const ls = second(); if (ls && ls.length) writeSync(fd, ls.join('\n') + '\n'); return ls; };
}
const news = [];
const t0 = performance.now();
while (!run.done) { run.step(600, 1e9); news.push(...run.snapshot().news); }
const r = run.finish();
news.push(...run.snapshot().news);
const wall = performance.now() - t0;
if (fd != null) closeSync(fd);
if (arg('--db')) writeFileSync(arg('--db'), run.exportFile());
out.today = { ...r, wallMs: wall, news };
for (const n of news.sort((a, b) => a.t - b.t)) log(`  ${clock(TODAY_START + n.t)}  ${n.text}`);
log(`\nSQLite ${r.sqlite}; ${r.lines.toLocaleString('en-US')} lines today on ${r.historyLines.toLocaleString('en-US')} lines of history in ${(wall / 1000).toFixed(1)} s; ` +
  `the reducer and Engine took ${(r.engineMs / 1000).toFixed(1)} s (${Math.round(r.lines / (r.engineMs / 1000)).toLocaleString('en-US')} lines a second)`);
log(`checks: ${r.checks}, one every 10 s, ${(r.checkMs / r.checks).toFixed(1)} ms on average, ${r.maxCheckMs.toFixed(1)} ms at most`);
const describe = (o) => {
  const parts = [o.detected == null ? 'never stood out' : `stood out after ${o.detected} s`];
  if (o.knownAtStart) parts.push(o.named == null ? 'NEVER NAMED' : `named #${o.namedAs} after ${o.named} s`);
  else parts.push(o.newAt == null ? 'NOT FLAGGED NEW' : `flagged new after ${o.newAt} s`);
  if (o.wrong) parts.push(`named wrong ${o.wrong} times (#${o.wrongAs})`);
  return parts.join(', ');
};
for (const o of r.outcomes) log(`  ${clock(TODAY_START + o.at)} ${o.name.padEnd(36)} ${describe(o)}  ${o.ok ? 'OK' : 'MISSED'}`);
log(`episodes: ${r.episodes}; false alarms: ${r.falseAlarms.length}${r.falseAlarms.map((e) => ` ${clock(TODAY_START + e.start)}`).join('')}`);
log(`recount: ${r.counts.same.toLocaleString('en-US')} of ${r.counts.cells.toLocaleString('en-US')} counts by minute and hour equal${r.counts.first ? `; first difference ${r.counts.first}` : ''}`);
log(`file ${(r.fileBytes / 1e6).toFixed(2)} MB; ${r.templates} templates; ${r.incidents.length} incidents written down`);
for (const t of r.tables) log(`  ${t.name.padEnd(36)} ${String(t.rows ?? '').padStart(8)} ${String(t.bytes).padStart(10)}`);

// More days: other seeds, incidents started at random, none of them scripted.
const trials = Number(arg('--trials') || 0);
if (trials) {
  const KNOWN = [['payments', { provider: 'northpay' }], ['payments', { provider: 'quickcard' }], ['attack'], ['search'], ['dbpool'], ['cache']];
  const NEW = [['tls', { host: 'img.cdn-c.example' }], ['queue']];
  const agg = { known: 0, named: 0, wrong: 0, newKinds: 0, flagged: 0, falseAlarms: 0, delays: [], checkMs: 0, checks: 0, days: [] };
  for (let i = 1; i <= trials; i++) {
    const rnd = mulberry32(7000 + i);
    const kinds = [...KNOWN].sort(() => rnd() - 0.5).slice(0, 4);
    kinds.splice(Math.floor(rnd() * 5), 0, NEW[Math.floor(rnd() * NEW.length)]);
    let at = 300 + Math.floor(rnd() * 600);
    const plan = kinds.map(([kind, opts]) => {
      const x = { kind, opts: opts || {}, at };
      at += KINDS[kind].minutes * 60 + 600 + Math.floor(rnd() * 300);
      return x;
    }).filter((x) => x.at + KINDS[x.kind].minutes * 60 + 300 < TODAY_DURATION);
    const tr = await new Run(sqlite3, module, shipped, { seed: 1000 + i, script: false }).init();
    for (const x of plan) {
      while (tr.t < x.at) tr.step(x.at - tr.t, 1e9);
      tr.start(x.kind, x.opts);
    }
    const res = tr.finish();
    tr.close();
    const day = { seed: 1000 + i, incidents: res.outcomes.map((o) => ({ key: o.key, at: o.at, known: o.knownAtStart, detected: o.detected, named: o.named, newAt: o.newAt, wrong: o.wrong, ok: o.ok })),
      falseAlarms: res.falseAlarms.length, alarms: res.falseAlarms };
    agg.days.push(day);
    for (const o of res.outcomes) {
      if (o.knownAtStart) { agg.known++; if (o.named != null) { agg.named++; agg.delays.push(o.named); } }
      else { agg.newKinds++; if (o.newAt != null && !o.wrong) agg.flagged++; }
      agg.wrong += o.wrong ? 1 : 0;
    }
    agg.falseAlarms += res.falseAlarms.length;
    agg.checkMs += res.checkMs;
    agg.checks += res.checks;
    for (const e of res.falseAlarms) log(`    false alarm ${clock(TODAY_START + e.start)} to ${clock(TODAY_START + e.last)} (${JSON.stringify(e.states)}): ${e.what.map((w) => `${w.service}/${w.level} "${w.text}" ${w.now.toFixed(1)} vs ${w.usual}`).join('; ')}; incidents at ${day.incidents.map((o) => clock(TODAY_START + o.at)).join(' ')}`);
    log(`  trial ${i}: ${day.incidents.map((o) => `${o.key}${o.known ? (o.named != null ? ` named ${o.named}s` : ' MISSED') : (o.newAt != null ? ` new ${o.newAt}s` : ' MISSED')}${o.wrong ? ` wrong x${o.wrong}` : ''}`).join(', ')}; false alarms ${day.falseAlarms}`);
  }
  agg.delays.sort((a, b) => a - b);
  const med = agg.delays.length ? agg.delays[Math.floor(agg.delays.length / 2)] : null;
  out.trials = { ...agg, median: med, max: agg.delays[agg.delays.length - 1] ?? null };
  log(`trials: ${trials} days; known kinds named correctly ${agg.named} of ${agg.known} (median ${med} s, at most ${out.trials.max} s after they began); ` +
    `new kinds flagged new ${agg.flagged} of ${agg.newKinds}; incidents ever named wrong ${agg.wrong}; false alarms ${agg.falseAlarms}; checks ${(agg.checkMs / agg.checks).toFixed(1)} ms on average`);
}
if (json) console.log(JSON.stringify(out, (k, v) => (k === 'news' && !process.argv.includes('--news') ? undefined : v), 1));
process.exit(0);
