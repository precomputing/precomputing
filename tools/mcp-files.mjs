// Makes the files Demo 7 asks, and the facts its answers are checked against.
//
// The files are the four demos' own, as their headless runs leave them (run-demo1.mjs to
// run-demo4.mjs with --db), each distilled by its own policy as it would be once its short-lived
// detail has faded: Demo 1 a day after its three hours, Demo 2 an hour after the close, Demo 3 on
// 31 December when September's dispute window ends, Demo 4 two days after its two hours. The
// answers do not change; the raw events behind them are gone, which is the point.
//
// The facts come from the raw events, by separate code that never reads a precomputed answer:
// the demos' own event generators for Demos 1, 2 and 4, and for Demo 3 the requests the Meter's
// file kept whole and the invoices its recount checked (demo3.json from run-demo3.mjs --json).
//
// Usage: node tools/mcp-files.mjs DIR    (DIR holds latency.sqlite, trades.sqlite, usage.sqlite,
//        shop.sqlite and demo3.json; writes demo/mcp/app/data/)
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { Scenario as Latency, START as LAT_START } from '../demo/sql/app/scenario.js';
import { Market, OPEN, SYMBOLS } from '../demo/engine/app/market.js';
import { Shop, START as SHOP_START } from '../demo/logs/app/scenario.js';
import { START as SEPT, MONTH_END } from '../demo/meter/app/scenario.js';

const dir = process.argv[2] || 'build/files';
const out = new URL('../demo/mcp/app/data/', import.meta.url);
mkdirSync(out, { recursive: true });
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const { capi, oo1, wasm } = sqlite3;

function open(path) {
  const bytes = new Uint8Array(readFileSync(path));
  const db = new oo1.DB(':memory:', 'c');
  const p = wasm.allocFromTypedArray(bytes);
  db.checkRc(capi.sqlite3_deserialize(db.pointer, 'main', p, bytes.byteLength, bytes.byteLength,
    capi.SQLITE_DESERIALIZE_FREEONCLOSE | capi.SQLITE_DESERIALIZE_RESIZEABLE));
  return { db, bytes: bytes.byteLength };
}

const H = 3600, D = 86400;
const FILES = [
  { name: 'latency', demo: 1, title: 'API traffic (Demo 1)', fadeAt: LAT_START + 3 * H + D, when: 'a day after its three hours' },
  { name: 'trades', demo: 2, title: 'A trading day (Demo 2)', fadeAt: OPEN + 23400 + H, when: 'an hour after the close' },
  { name: 'usage', demo: 3, title: 'A month of AI usage (Demo 3)', fadeAt: MONTH_END + D + 90 * D, when: 'on 31 December, when September\'s dispute window ends' },
  { name: 'shop', demo: 4, title: 'A web shop\'s logs (Demo 4)', fadeAt: SHOP_START + 7200 + 2 * D, when: 'two days after its two hours' },
];

const facts = { files: [], questions: {} };
const full = {};
for (const f of FILES) {
  const { db, bytes } = open(`${dir}/${f.name}.sqlite`);
  full[f.name] = db;
  const copy = open(`${dir}/${f.name}.sqlite`).db;
  const distill = copy.selectValue("SELECT value FROM _precomputing WHERE key = 'distill'");
  copy.exec(distill.replaceAll(':now', String(f.fadeAt)));
  copy.exec('VACUUM');
  const faded = capi.sqlite3_js_db_export(copy.pointer);
  copy.close();
  const gz = gzipSync(faded, { level: 9 });
  writeFileSync(new URL(`${f.name}.sqlite.gz`, out), gz);
  facts.files.push({ ...f, fullBytes: bytes, fadedBytes: faded.byteLength, gzBytes: gz.byteLength, fadedAtIso: new Date(f.fadeAt * 1000).toISOString() });
  console.log(`${f.name}: ${bytes} bytes at the end of the run, ${faded.byteLength} faded ${f.when}, ${gz.byteLength} compressed`);
}

const enc = new TextEncoder();
const bytesOf = (lines) => lines.reduce((a, l) => a + enc.encode(l).length + 1, 0);
const exactPct = (sorted, q) => sorted[Math.ceil(q * sorted.length) - 1];

// Demo 1: every request to /api/checkout from 10:00 to 11:00, from the demo's generator.
{
  const from = LAT_START + H, to = LAT_START + 2 * H;
  const sc = new Latency();
  const lines = ['ts,endpoint,ms'];
  const ms = [];
  for (let evs = sc.second(); evs; evs = sc.second()) {
    for (const [ts, ep, v] of evs) if (ep === '/api/checkout' && ts >= from && ts < to) { ms.push(v); lines.push(`${ts},${ep},${v}`); }
  }
  const sorted = [...ms].sort((a, b) => a - b);
  let sum = 0;
  for (const v of ms) sum += v;
  facts.questions.latency = {
    raw: { events: ms.length, bytes: bytesOf(lines), what: 'every request to /api/checkout from 10:00 to 11:00 UTC, as CSV lines of ts, endpoint and ms' },
    exact: { n: ms.length, avg: sum / ms.length, min: sorted[0], max: sorted[sorted.length - 1], first: ms[0], last: ms[ms.length - 1],
      p50: exactPct(sorted, 0.5), p95: exactPct(sorted, 0.95), p99: exactPct(sorted, 0.99) },
  };
}

// Demo 2: SIM4's trades in the minute from 10:30 New York time (14:30 UTC), from the market generator.
{
  const sym = SYMBOLS.indexOf('SIM4');
  const from = OPEN + H, to = from + 60;
  const m = new Market();
  const lines = ['ts,symbol,price,size'];
  const x = { n: 0, open: null, high: -Infinity, low: Infinity, close: null, volume: 0, notional: 0 };
  while (m.t < to - OPEN) {
    const o = m.second();
    if (o.ts < from) continue;
    for (let k = 0; k < o.n; k++) {
      if (o.sym[k] !== sym) continue;
      const p = o.price[k], s = o.size[k];
      lines.push(`${o.ts},SIM4,${p},${s}`);
      x.n++;
      x.open ??= p;
      x.close = p;
      x.high = Math.max(x.high, p);
      x.low = Math.min(x.low, p);
      x.volume += s;
      x.notional += p * s;
    }
  }
  facts.questions.trades = {
    raw: { events: x.n, bytes: bytesOf(lines), what: "SIM4's trades from 10:30 to 10:31 New York time, as CSV lines of ts, symbol, price and size" },
    exact: { ...x, vwap: x.notional / x.volume },
  };
}

// Demo 3: Harbor Legal Drafts' September requests, as the Meter's file kept them whole until the
// dispute window ended, and the invoice the demo's recount checked to the nano-dollar.
{
  const db = full.usage;
  const rows = db.selectArrays("SELECT ts, request_id, customer, model, gateway, input_tokens, output_tokens FROM usage_raw WHERE customer = 'harbor' AND ts >= ? AND ts < ? ORDER BY ts", [SEPT, MONTH_END]);
  const lines = ['ts,request_id,customer,model,gateway,input_tokens,output_tokens', ...rows.map((r) => r.join(','))];
  const d3 = JSON.parse(readFileSync(`${dir}/demo3.json`, 'utf8'));
  const inv = d3.invoices.find((i) => i.customer === 'harbor');
  facts.questions.usage = {
    raw: { events: rows.length, bytes: bytesOf(lines), what: "Harbor Legal Drafts' September requests, as CSV lines of ts, request_id, customer, model, gateway, input_tokens and output_tokens" },
    exact: { requests: inv.requests, input_tokens: inv.input_tokens, output_tokens: inv.output_tokens, list_nano: String(inv.list_nano), due_nano: String(inv.due_nano),
      due_cents: inv.due_cents, cost_nano: String(inv.cost_nano), recountOk: inv.ok },
  };
}

// Demo 4: every line written from 12:40 on, from the shop's generator, and the kinds of line
// that first appear then. A kind is the level, the service and the message's leading words,
// read straight from the lines, with no template miner involved.
{
  const shop = new Shop();
  const from = 40 * 60;
  const kinds = new Map();
  const lines = [];
  const kindOf = (line) => {
    const [, , level, service, message] = line.match(/^(\S+) (\S+)\s+(\S+) (.*)$/);
    const words = message.split(' ').filter((w) => !/[=/0-9]/.test(w)).slice(0, 2);
    return `${level} ${service} ${words.join(' ')}`;
  };
  for (let ls = shop.second(); ls; ls = shop.second()) {
    const t = shop.t - 1;
    for (const l of ls) {
      const k = kindOf(l);
      let e = kinds.get(k);
      if (!e) kinds.set(k, (e = { kind: k, first: l.slice(0, 24), firstT: t, lines: 0 }));
      e.lines++;
      if (t >= from) lines.push(l);
    }
  }
  const fresh = [...kinds.values()].filter((k) => k.firstT >= from).sort((a, b) => a.first.localeCompare(b.first));
  facts.questions.shop = {
    raw: { events: lines.length, bytes: bytesOf(lines), what: 'every log line the shop wrote from 12:40 to 14:00 UTC' },
    exact: { kinds: kinds.size, fresh: fresh.map(({ kind, first, lines }) => ({ kind, first, lines })) },
  };
}

writeFileSync(new URL('facts.json', out), JSON.stringify(facts, null, 1) + '\n');
for (const [k, q] of Object.entries(facts.questions)) console.log(`${k}: raw ${q.raw.events} events, ${q.raw.bytes} bytes`, JSON.stringify(q.exact));
