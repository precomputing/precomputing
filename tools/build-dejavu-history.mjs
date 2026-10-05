// Builds demo/dejavu/app/history.db.gz: the web shop's last 24 hours through the log reducer and the
// Engine (the same Go program and SQLite build the page uses), with the incidents the on-call team
// labelled and the views that compare incidents. Run by tools/build-demos.sh.
// Usage: node tools/build-dejavu-history.mjs [--out FILE]
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { createHash } from 'node:crypto';
import { loadGo, startGo } from '../demo/lib/engine.js';
import { buildHistory } from '../demo/dejavu/app/history.js';

const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const root = new URL('..', import.meta.url);
const read = (f) => readFileSync(new URL(f, root), 'utf8');
const out = arg('--out') || new URL('demo/dejavu/app/history.db.gz', root);
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const go = await startGo(await loadGo(new URL('demo/lib/go/', root).href));
const t0 = performance.now();
const h = buildHistory(sqlite3, go, read('examples/dejavu.precompute'), read('demo/dejavu/app/dejavu.sql'), {
  onProgress: (f) => process.stderr.write(`\r${Math.round(f * 100)}%`),
});
const gz = gzipSync(h.bytes, { level: 9 });
writeFileSync(out, gz);
const s = (performance.now() - t0) / 1000;
process.stderr.write('\r');
console.log(`history: ${h.lines.toLocaleString('en-US')} lines (${(h.rawBytes / 1e6).toFixed(1)} MB) in ${s.toFixed(1)} s, ` +
  `${h.templates} templates; file ${(h.bytes.byteLength / 1e6).toFixed(2)} MB, gzipped ${(gz.byteLength / 1e6).toFixed(2)} MB, ` +
  `sha256 ${createHash('sha256').update(h.bytes).digest('hex').slice(0, 16)}`);
process.exit(0);
