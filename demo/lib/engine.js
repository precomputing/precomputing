// The Engine in the browser: the Go build of Precomputing, given a store over SQLite's own
// WebAssembly build. Works in a module worker, on a page, and in Node.

let goReady = null;

// loadGo fetches and compiles precomputing.wasm once; startGo can then start any number of
// fresh copies of the program from it, each with its own memory.
export function loadGo(base) {
  goReady ??= (async () => {
    await import(base + 'wasm_exec.js');
    const url = base + 'precomputing.wasm';
    if (typeof process !== 'undefined' && process.versions?.node) {
      const { readFile } = await import('node:fs/promises');
      return WebAssembly.compile(await readFile(new URL(url)));
    }
    const res = await fetch(url);
    if (!res.ok) throw new Error(`precomputing.wasm: HTTP ${res.status}`);
    return WebAssembly.compile(await res.arrayBuffer());
  })();
  return goReady;
}

// startGo starts a fresh copy of the Go program and resolves to its API: {compile, open, version}.
export async function startGo(module) {
  const go = new globalThis.Go();
  const api = new Promise((resolve) => { globalThis.__precomputingReady = resolve; });
  const instance = await WebAssembly.instantiate(module, go.importObject);
  go.run(instance);
  return api;
}

const dec = new TextDecoder();

// countRows adds up the rows of every block of a checkpoint, starting at byte p.
function countRows(u8, p, nBlocks) {
  const dv = new DataView(u8.buffer, u8.byteOffset, u8.byteLength);
  const u32 = () => { const v = dv.getUint32(p, true); p += 4; return v; };
  const skip = (size) => { const n = u32(); if (size === 8) p = Math.ceil(p / 8) * 8; p += n * size; if (size === 1) p = Math.ceil(p / 4) * 4; };
  let rows = 0;
  for (let b = 0; b < nBlocks; b++) {
    skip(1); skip(1);
    rows += u32();
    skip(8); skip(8); skip(1);
    const offs = u32(); p += 4 * offs;
    const lens = u32(); p += 4 * lens;
  }
  return rows;
}

// sqliteReader lets the MCP server of the Go build read a file held by SQLite WebAssembly, the
// way the native server reads one through a read-only connection: one statement at a time, reads
// only, stopped after five seconds. Errors read like the native build's.
export function sqliteReader(sqlite3, db, timeoutMs = 5000) {
  const capi = sqlite3.capi;
  const clean = (e, where) => {
    const m = String(e.message || e).replace(/^SQLITE_[A-Z_]+: sqlite3 result code \d+: /, '');
    return `${where}: ${m}`;
  };
  return {
    read(sql, args, max) {
      let st = null;
      try {
        try { st = db.prepare(sql); } catch (e) { return { error: clean(e, 'prepare') }; }
        if (!capi.sqlite3_stmt_readonly(st.pointer)) return { error: 'only reading is allowed; the file changes through its streams' };
        if (args && args.length) st.bind(args);
        const columns = st.getColumnNames();
        const rows = [];
        let more = false;
        const t0 = performance.now();
        let stopped = false;
        capi.sqlite3_progress_handler(db.pointer, 10000, () => (performance.now() - t0 > timeoutMs ? ((stopped = true), 1) : 0), 0);
        try {
          while (st.step()) {
            if (max > 0 && rows.length === max) { more = true; break; }
            const r = st.get([]);
            for (let i = 0; i < r.length; i++) if (typeof r[i] === 'bigint') r[i] = Number(r[i]);
            rows.push(r);
          }
        } catch (e) {
          if (stopped) return { error: `the read took longer than ${timeoutMs / 1000}s and was stopped; narrow it` };
          return { error: clean(e, 'step') };
        } finally {
          capi.sqlite3_progress_handler(db.pointer, 0, 0, 0);
        }
        return { columns, rows, more };
      } finally {
        if (st) st.finalize();
      }
    },
  };
}

// openReadOnly holds the bytes of an SQLite file in a database that refuses every write.
export function openReadOnly(sqlite3, bytes) {
  const { capi, oo1, wasm } = sqlite3;
  const db = new oo1.DB(':memory:', 'c');
  const p = wasm.allocFromTypedArray(bytes);
  const rc = capi.sqlite3_deserialize(db.pointer, 'main', p, bytes.byteLength, bytes.byteLength,
    capi.SQLITE_DESERIALIZE_FREEONCLOSE | capi.SQLITE_DESERIALIZE_READONLY);
  db.checkRc(rc);
  return db;
}

// sqliteStore lets the Engine keep its file in an SQLite WebAssembly database.
export function sqliteStore(sqlite3, db) {
  const X = sqlite3.wasm.exports;
  const cache = new Map();
  const stmt = (sql) => {
    let st = cache.get(sql);
    if (!st) { st = db.prepare(sql); cache.set(sql, st); }
    return st.pointer;
  };
  const fail = (what) => new Error(`${what}: ${sqlite3.capi.sqlite3_errmsg(db.pointer)}`);
  return {
    exec(sql) {
      try { db.exec(sql); return null; } catch (e) { return String(e.message || e); }
    },
    query(sql, args) {
      try {
        const rows = db.exec({ sql, bind: args && args.length ? args : undefined, rowMode: 'array', returnValue: 'resultRows' });
        for (const r of rows) for (let i = 0; i < r.length; i++) if (typeof r[i] === 'bigint') r[i] = Number(r[i]);
        return { rows };
      } catch (e) {
        return { error: String(e.message || e) };
      }
    },
    // apply writes one checkpoint (the layout is described in wasm/precomputing/main.go).
    apply(u8) {
      const buf = u8.buffer, base = u8.byteOffset;
      const dv = new DataView(buf, base, u8.byteLength);
      let p = 0;
      const u32 = () => { const v = dv.getUint32(p, true); p += 4; return v; };
      const pad = (to) => { p = Math.ceil(p / to) * to; };
      const bytes = () => { const n = u32(); const s = u8.subarray(p, p + n); p += n; pad(4); return s; };
      const f64s = () => { const n = u32(); pad(8); const a = new Float64Array(buf, base + p, n); p += 8 * n; return a; };
      const i32s = () => { const n = u32(); const a = new Int32Array(buf, base + p, n); p += 4 * n; return a; };
      if (dec.decode(u8.subarray(0, 4)) !== 'PCB1') return 'not a checkpoint';
      p = 4;
      const nBlocks = u32(), nDistill = u32();
      u32();
      const now = dv.getFloat64(p, true); p += 8;
      // A pulled plug (see cut) stops the writing after this many rows, as a crash would.
      let cutRow = Infinity, written = 0;
      if (this.cut != null) cutRow = Math.floor(countRows(u8, p, nBlocks) * this.cut);
      db.exec('BEGIN');
      try {
        for (let b = 0; b < nBlocks; b++) {
          const sql = dec.decode(bytes()), types = bytes(), n = u32();
          const ints = f64s(), reals = f64s(), text = bytes(), offs = i32s(), lens = i32s();
          const st = stmt(sql);
          let tp = 0;
          if (text.length) { tp = sqlite3.wasm.alloc(text.length); sqlite3.wasm.heap8u().set(text, tp); }
          try {
            let ii = 0, fi = 0, ti = 0;
            for (let r = 0; r < n; r++) {
              if (written++ === cutRow) {
                this.cutRows = cutRow;
                throw new Error(`the power went out after ${cutRow} rows of this checkpoint`);
              }
              for (let c = 0; c < types.length; c++) {
                const t = types[c];
                if (t === 105) { // integer: exact as a double below 2^53; INTEGER columns store it as an integer
                  const v = ints[ii++];
                  if ((v | 0) === v) X.sqlite3_bind_int(st, c + 1, v); else X.sqlite3_bind_double(st, c + 1, v);
                } else if (t === 102) {
                  X.sqlite3_bind_double(st, c + 1, reals[fi++]);
                } else if (t === 116) {
                  X.sqlite3_bind_text(st, c + 1, tp + offs[ti], lens[ti], 0);
                  ti++;
                } else {
                  X.sqlite3_bind_null(st, c + 1);
                }
              }
              const rc = X.sqlite3_step(st);
              if (rc !== 101 && rc !== 100) { const err = fail(sql); X.sqlite3_reset(st); throw err; }
              X.sqlite3_reset(st);
            }
          } finally {
            X.sqlite3_clear_bindings(st);
            if (tp) sqlite3.wasm.dealloc(tp);
          }
        }
        for (let d = 0; d < nDistill; d++) {
          const sql = dec.decode(bytes());
          const st = stmt(sql);
          const i = sqlite3.capi.sqlite3_bind_parameter_index(st, ':now');
          if (i > 0) X.sqlite3_bind_double(st, i, now);
          const rc = X.sqlite3_step(st);
          if (rc !== 101 && rc !== 100) { const err = fail(sql); X.sqlite3_reset(st); throw err; }
          X.sqlite3_reset(st);
        }
        db.exec('COMMIT');
        return null;
      } catch (e) {
        try { db.exec('ROLLBACK'); } catch { /* the transaction is gone already */ }
        return String(e.message || e);
      }
    },
    // cut, when set to a fraction such as 0.5, makes the next checkpoint stop part way through,
    // the way a crash would stop it; the transaction is rolled back as SQLite would on restart.
    cut: null,
    cutRows: 0,
    // close finalizes the prepared statements.
    close() {
      for (const st of cache.values()) st.finalize();
      cache.clear();
    },
  };
}
