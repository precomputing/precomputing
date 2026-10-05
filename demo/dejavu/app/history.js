// Builds the file Demo 6 starts from: the shop's last 24 hours of logs through the log reducer and
// the Engine, with the tables, views and trigger that compare incidents (dejavu.sql) and the six
// incidents the on-call team wrote down, each three minutes after it began. tools/build-dejavu-history.mjs
// runs it in Node to make history.db.gz; the page can run it again to show the same lines give the
// same file.
import { historyShop, HISTORY_INCIDENTS } from './scenario.js';
import { sqliteStore } from '../../lib/engine.js';

export const NAME = 'dejavu.precompute';
export const HISTORY_CHECKPOINT = 60;     // simulated seconds between checkpoints while building
export const LABEL_AFTER = 180;           // the team writes each incident down this long after it began
export const HISTORY_SOURCE = 'on-call team';

// buildHistory returns the file's bytes and what went into it. go is a started Go program.
// onLines(lines) sees every second's lines; onCheck(db, ts) runs after every checkpoint.
export function buildHistory(sqlite3, go, policy, extraSql, { onProgress, onLines, onCheck } = {}) {
  const db = new sqlite3.oo1.DB(':memory:');
  const store = sqliteStore(sqlite3, db);
  const eng = go.logs(store, policy, NAME);
  if (eng.error) throw new Error(eng.error);
  db.exec(extraSql);
  const insert = db.prepare('INSERT INTO incidents (id, started, ended, title, fix, source) VALUES (?, ?, ?, ?, ?, ?)');
  const labels = HISTORY_INCIDENTS.map((x, i) => ({ ...x, id: i + 1, due: x.at + LABEL_AFTER }));
  const shop = historyShop();
  let seq = 0;
  let rawBytes = 0;
  for (;;) {
    const lines = shop.second();
    if (!lines) break;
    if (lines.length) {
      for (const l of lines) rawBytes += l.length + 1;
      if (onLines) onLines(lines);
      const r = eng.putLines(lines.join('\n'), 'shop', seq + 1);
      if (r.error) throw new Error(r.error);
      seq += lines.length;
    }
    if (shop.t % HISTORY_CHECKPOINT === 0) {
      const c = eng.checkpoint();
      if (c.error) throw new Error(c.error);
      for (const x of labels) {
        if (x.due !== shop.ts) continue;
        insert.bind([x.id, x.at, x.at + x.minutes * 60, x.title, x.fix, HISTORY_SOURCE]).stepReset();
      }
      if (onCheck) onCheck(db, shop.ts);
      if (onProgress && shop.t % 3600 === 0) onProgress(shop.t / shop.seconds);
    }
  }
  insert.finalize();
  const c = eng.checkpoint();
  if (c.error) throw new Error(c.error);
  const templates = (eng.templates() || []).length;
  eng.close();
  store.close();
  db.exec('VACUUM');
  const bytes = sqlite3.capi.sqlite3_js_db_export(db.pointer);
  db.close();
  return { bytes, lines: seq, rawBytes, templates };
}

// openFile loads a file's bytes into a fresh in-memory database.
export function openFile(sqlite3, bytes) {
  const db = new sqlite3.oo1.DB(':memory:', 'c');
  const p = sqlite3.wasm.allocFromTypedArray(bytes);
  const rc = sqlite3.capi.sqlite3_deserialize(db.pointer, 'main', p, bytes.byteLength, bytes.byteLength,
    sqlite3.capi.SQLITE_DESERIALIZE_FREEONCLOSE | sqlite3.capi.SQLITE_DESERIALIZE_RESIZEABLE);
  db.checkRc(rc);
  return db;
}

// gunzip returns the bytes of a file that may or may not be gzipped (a server may have unpacked it).
export async function gunzip(bytes) {
  if (bytes[0] !== 0x1f || bytes[1] !== 0x8b) return bytes;
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}
