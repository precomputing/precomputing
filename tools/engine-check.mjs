// Runs the start of the Demo 2 day through the Engine in WebAssembly and through the compiled
// triggers in SQLite's WebAssembly build, then checks that both files hold the same rows, bit for bit.
// Usage: node tools/engine-check.mjs [seconds of the day, default 1800]
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync } from 'node:fs';
import { loadGo, startGo, sqliteStore } from '../demo/lib/engine.js';
import { Market, SYMBOLS } from '../demo/engine/app/market.js';

const seconds = Number(process.argv[2] || 1800);
const root = new URL('..', import.meta.url);
const policy = readFileSync(new URL('examples/trades.precompute', root), 'utf8');
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const go = await startGo(await loadGo(new URL('demo/lib/go/', root).href));

// The day's trades, kept to feed both runtimes the same input.
const market = new Market();
const days = [];
let total = 0;
for (let t = 0; t < seconds; t++) { const o = market.second(); days.push(o); total += o.n; }

// Engine.
const edb = new sqlite3.oo1.DB(':memory:');
const store = sqliteStore(sqlite3, edb);
const eng = go.open(store, policy, 'trades.precompute');
if (eng.error) throw new Error(eng.error);
const si = eng.stream('trades');
const keyIds = SYMBOLS.map((s) => eng.key(si, s));
let seq = 1, sinceCk = 0, ckMs = 0, cks = 0;
const t0 = performance.now();
for (const o of days) {
  const rows = new Float64Array(o.n * 4);
  for (let i = 0; i < o.n; i++) {
    rows[4 * i] = o.ts; rows[4 * i + 1] = keyIds[o.sym[i]]; rows[4 * i + 2] = o.price[i]; rows[4 * i + 3] = o.size[i];
  }
  const r = eng.put(si, 'feed', seq, o.n, new Uint8Array(rows.buffer));
  if (r.error) throw new Error(r.error);
  seq += o.n; sinceCk += o.n;
  if (sinceCk >= 10000) {
    const c = eng.checkpoint();
    if (c.error) throw new Error(c.error);
    ckMs += c.ms; cks++; sinceCk = 0;
  }
}
const c = eng.checkpoint();
if (c.error) throw new Error(c.error);
ckMs += c.ms; cks++;
const engMs = performance.now() - t0;

// Triggers.
const compiled = go.compile(policy, 'trades.precompute');
const sdb = new sqlite3.oo1.DB(':memory:');
sdb.exec(compiled.schema);
const distill = compiled.distill.split('\n').filter((l) => l.startsWith('DELETE'));
const ins = sdb.prepare('INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?)');
const X = sqlite3.wasm.exports;
const t1 = performance.now();
let now = 0, n = 0;
sdb.exec('BEGIN');
for (const o of days) {
  for (let i = 0; i < o.n; i++) {
    ins.bind(1, o.ts);
    ins.bind(2, SYMBOLS[o.sym[i]]);
    X.sqlite3_bind_double(ins.pointer, 3, o.price[i]);
    ins.bind(4, o.size[i]);
    ins.step();
    ins.reset();
    if (++n % 10000 === 0) for (const d of distill) sdb.exec({ sql: d, bind: { ':now': o.ts } });
  }
  now = o.ts;
}
for (const d of distill) sdb.exec({ sql: d, bind: { ':now': now } });
sdb.exec('COMMIT');
ins.finalize();
const sqlMs = performance.now() - t1;

// Compare every table except the Engine's own list of senders.
const tables = sdb.selectValues("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name");
const f = new Float64Array(2), u = new BigUint64Array(f.buffer);
const same = (a, b) => {
  if (typeof a === 'number' && typeof b === 'number') { f[0] = a; f[1] = b; return u[0] === u[1]; }
  return a === b;
};
let rowsCompared = 0, bad = 0;
for (const name of tables) {
  const sqlText = sdb.selectValue('SELECT sql FROM sqlite_master WHERE name = ?', name);
  const ncol = sdb.selectArray(`SELECT * FROM ${name} LIMIT 1`)?.length ?? 1;
  const order = /WITHOUT ROWID/.test(sqlText) ? Array.from({ length: ncol }, (_, i) => i + 1).join(', ') : 'rowid';
  const q = `SELECT *, typeof(${name}.rowid) FROM ${name} ORDER BY ${order}`.replace(`, typeof(${name}.rowid)`, '');
  const a = sdb.exec({ sql: q, rowMode: 'array', returnValue: 'resultRows' });
  const b = edb.exec({ sql: q, rowMode: 'array', returnValue: 'resultRows' });
  rowsCompared += a.length;
  let diff = a.length !== b.length ? Math.abs(a.length - b.length) : 0;
  if (!diff) for (let i = 0; i < a.length; i++) for (let j = 0; j < a[i].length; j++) if (!same(a[i][j], b[i][j])) { if (diff < 2) console.log(name, 'row', i, 'col', j, a[i][j], b[i][j]); diff++; }
  if (diff) bad++;
  console.log(`${name.padEnd(30)} ${String(a.length).padStart(7)} rows ${diff ? `${diff} DIFFER` : 'identical'}`);
}
const st = eng.stats();
console.log(`\n${total} trades (${seconds} s of the day)`);
console.log(`Engine:   ${engMs.toFixed(0)} ms = ${Math.round(total / engMs * 1000)} trades/s; ${cks} checkpoints took ${ckMs.toFixed(0)} ms; ${st.rowsWritten} rows written`);
console.log(`Triggers: ${sqlMs.toFixed(0)} ms = ${Math.round(total / sqlMs * 1000)} trades/s`);
console.log(`${rowsCompared} rows compared: ${bad ? bad + ' tables differ' : 'every table identical'}`);
process.exit(bad ? 1 : 0);
