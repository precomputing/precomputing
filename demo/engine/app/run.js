// Demo 2: the trading day fed to the Engine, which keeps its file in SQLite's WebAssembly build.
// Runs in the page's worker and, unchanged, in Node (tools/run-demo2.mjs makes the published numbers).
//
// The feed numbers every trade and keeps each one until a checkpoint acknowledges it. When the
// plug is pulled, the Engine's memory (the whole Go program) is thrown away in the middle of a
// checkpoint, a fresh copy opens the file, and the feed resends everything after the last
// acknowledged number. An independent recount of every candle checks the file afterwards.
import { Market, SYMBOLS, OPEN, DAY } from './market.js';
import { startGo, sqliteStore } from '../../lib/engine.js';

export const CHECKPOINT_MS = 200;     // the Engine writes its file at least this often (real time)
export const SCRIPTED_PLUG = 13500;   // 13:15 New York: the demo pulls the plug once by itself
export const RACE_TRADES = 200000;    // the race runs the first trades of the day through both runtimes
const RES = [1, 60, 3600];
const RAW_KEEP = 300;
const ONE_SECOND_KEEP = 3600;

const now = () => performance.now();

// pack lays a second's trades out for the Engine: ts, key id, price, size per trade.
function pack(o, keys) {
  const rows = new Float64Array(o.n * 4);
  for (let i = 0; i < o.n; i++) {
    rows[4 * i] = o.ts;
    rows[4 * i + 1] = keys[o.sym[i]];
    rows[4 * i + 2] = o.price[i];
    rows[4 * i + 3] = o.size[i];
  }
  return rows;
}

// Expected keeps every candle and the quote board, counted straight from the trades as they
// are made, with the same arithmetic in the same order as the policy asks for. It never sees
// the Engine or the file.
class Expected {
  constructor() {
    this.win = RES.map(() => new Map());
    this.quote = SYMBOLS.map(() => null);
    this.trades = 0;
    this.raw = [];                     // the last five minutes of trades, as the raw tier keeps them
  }

  add(o) {
    for (let i = 0; i < o.n; i++) {
      const s = o.sym[i], p = o.price[i], z = o.size[i], nv = p * z;
      for (let r = 0; r < RES.length; r++) {
        const res = RES[r];
        const w = o.ts - (o.ts % res);
        const key = w * 8 + s;
        const m = this.win[r];
        const c = m.get(key);
        if (!c) m.set(key, { w, s, n: 1, o: p, h: p, l: p, c: p, v: z, nv, ps: p });
        else {
          c.n++;
          if (!(c.l < p)) c.l = p;
          if (c.h < p) c.h = p;
          c.c = p;
          c.v = c.v + z;
          c.nv = c.nv + nv;
          c.ps = c.ps + p;
        }
      }
      const q = this.quote[s];
      if (!q) this.quote[s] = { o: p, h: p, l: p, c: p, v: z, nv, n: 1 };
      else {
        q.n++;
        if (!(q.l < p)) q.l = p;
        if (q.h < p) q.h = p;
        q.c = p;
        q.v = q.v + z;
        q.nv = q.nv + nv;
      }
      this.raw.push(o.ts, s, p, z);
    }
    this.trades += o.n;
    // Drop what the file no longer keeps: 1-second candles after an hour, raw trades after five minutes.
    const old = o.ts - ONE_SECOND_KEEP - 60;
    if (o.ts % 60 === 0) for (const [k, c] of this.win[0]) if (c.w < old) this.win[0].delete(k);
    let cut = 0;
    while (cut < this.raw.length && this.raw[cut] < o.ts - RAW_KEEP - 60) cut += 4;
    if (cut > 40000) this.raw = this.raw.slice(cut);
  }
}

export class Run {
  // module: the compiled Go program (loadGo); policy: the policy text.
  constructor(sqlite3, module, policy, name = 'trades.precompute') {
    this.sqlite3 = sqlite3;
    this.module = module;
    this.policy = policy;
    this.name = name;
    this.db = null;
    this.go = null;
  }

  async init() {
    this.go = await startGo(this.module);
    this.compiled = this.go.compile(this.policy, this.name);
    if (this.compiled.error) throw new Error(this.compiled.error);
    await this.reset();
    return this;
  }

  async reset() {
    if (this.eng) this.eng.close();
    if (this.store) this.store.close();
    if (this.db) this.db.close();
    this.db = new this.sqlite3.oo1.DB(':memory:');
    this.openEngine(this.policy);
    this.market = new Market();
    this.expected = new Expected();
    this.seq = 0;                // the last sequence number given out
    this.buffer = [];            // seconds of trades not yet acknowledged: {first, n, o}
    this.trades = 0;
    this.engineMs = 0;           // time spent inside the Engine, feeding and checkpointing
    this.checkpointMs = 0;
    this.checkpoints = 0;
    this.lastCheckpoint = { rows: 0, ms: 0, at: 0 };
    this.lastCkAt = now();
    this.plugs = [];
    this.armed = null;
    this.scriptedPlugDone = false;
    this.jumps = [];
    this.done = false;
    this.maxPending = 0;
  }

  openEngine(policy) {
    this.store = sqliteStore(this.sqlite3, this.db);
    const eng = this.go.open(this.store, policy, this.name);
    if (eng.error) throw new Error(eng.error);
    this.eng = eng;
    this.si = eng.stream('trades');
    this.keys = SYMBOLS.map((s) => eng.key(this.si, s));
  }

  get t() { return this.market.t; }

  // The visitor's button: a symbol's price jumps by a factor at the next second.
  jump(sym, factor) {
    this.market.jump(sym, factor);
    this.jumps.push({ t: this.market.t, sym, factor });
    return { t: this.market.t, symbol: SYMBOLS[sym], factor };
  }

  // armPlug makes the next checkpoint the moment the plug is pulled.
  armPlug(by) { if (!this.armed) this.armed = { by, armedAt: this.market.t }; }

  // step feeds up to maxSeconds simulated seconds, and stops once budgetMs of real time is spent.
  async step(maxSeconds, budgetMs) {
    const until = now() + budgetMs;
    for (let k = 0; k < maxSeconds && this.market.t < DAY; k++) {
      if (this.market.t === SCRIPTED_PLUG && !this.scriptedPlugDone) {
        this.scriptedPlugDone = true;
        this.armPlug('script');
      }
      const o = this.market.second();
      this.expected.add(o);
      const rows = pack(o, this.keys);
      const first = this.seq + 1;
      this.seq += o.n;
      this.buffer.push({ first, n: o.n, o });
      const t0 = now();
      const r = this.eng.put(this.si, 'feed', first, o.n, new Uint8Array(rows.buffer));
      this.engineMs += now() - t0;
      if (r.error) throw new Error(r.error);
      this.trades += o.n;
      const pending = this.seq - this.eng.committed('feed');
      if (pending > this.maxPending) this.maxPending = pending;
      if (now() - this.lastCkAt >= CHECKPOINT_MS) await this.checkpoint();
      if (now() > until) break;
    }
    if (this.market.t >= DAY && !this.done) {
      await this.checkpoint();
      if (this.armed) await this.checkpoint();
      this.done = true;
    }
  }

  async checkpoint() {
    if (this.armed) return this.pullPlug();
    const t0 = now();
    const c = this.eng.checkpoint();
    const ms = now() - t0;
    if (c.error) throw new Error(c.error);
    this.engineMs += ms;
    this.checkpointMs += ms;
    this.checkpoints++;
    this.lastCheckpoint = { rows: c.rows, ms, at: this.market.t };
    this.lastCkAt = now();
    // The file has everything up to the committed number: the feed can forget it.
    const committed = this.eng.committed('feed');
    let drop = 0;
    while (drop < this.buffer.length && this.buffer[drop].first + this.buffer[drop].n - 1 <= committed) drop++;
    if (drop) this.buffer.splice(0, drop);
    // After a recovery, check the candles once the file has caught up.
    for (const p of this.plugs) if (!p.check && committed >= p.appliedBefore) p.check = this.verify();
  }

  // pullPlug stops the Engine half way through a checkpoint and starts a new one from the file.
  async pullPlug() {
    const a = this.armed;
    this.armed = null;
    const committedBefore = this.eng.committed('feed');
    const appliedBefore = this.eng.applied('feed');
    this.store.cut = 0.5;
    const c = this.eng.checkpoint();
    const cutRows = c.error ? this.store.cutRows : 0; // with nothing to write, the plug is pulled just after
    // The Go program and everything in its memory are dropped, as in a crash.
    this.eng = null;
    this.go = null;
    this.store.close();
    const t0 = now();
    this.go = await startGo(this.module);
    this.openEngine(null);                    // the file carries its own policy
    const committed = this.eng.committed('feed');
    const openMs = now() - t0;
    // The feed resends everything after the last acknowledged number.
    let resent = 0;
    for (const b of this.buffer) {
      if (b.first + b.n - 1 <= committed) continue;
      const r = this.eng.put(this.si, 'feed', b.first, b.n, new Uint8Array(pack(b.o, this.keys).buffer));
      if (r.error) throw new Error(r.error);
      resent += r.applied;
    }
    const ms = now() - t0;
    this.engineMs += ms;
    this.lastCkAt = now();
    const p = {
      i: this.plugs.length, by: a.by, t: this.market.t, committedBefore, appliedBefore, lost: appliedBefore - committedBefore,
      cutRows, committed, resent, openMs, recoverMs: ms, check: null,
    };
    this.plugs.push(p);
    return p;
  }

  // verify compares every closed candle in the file with the recount, plus the last hour of
  // 1-second candles. Only the file's committed state is read.
  verify() {
    const t0 = now();
    const upTo = this.eng.now() - (this.eng.now() % 60);     // a whole minute the file has fully
    let checked = 0, diffs = 0;
    const byRes = {};
    const exp = this.expected;
    for (let r = 0; r < RES.length; r++) {
      const res = RES[r];
      const lo = res === 1 ? upTo - 3000 : 0;
      const hi = res === 3600 ? upTo - (upTo % 3600) : upTo;
      const file = this.db.exec({
        sql: `SELECT w, symbol, n, price_first, price_max, price_min, price_last, size_sum, notional_sum, price_sum
              FROM trades_win WHERE res = ? AND w >= ? AND w + ? <= ?`,
        bind: [res, lo, res, hi], rowMode: 'array', returnValue: 'resultRows',
      });
      const want = new Map();
      for (const c of exp.win[r].values()) if (c.w >= lo && c.w + res <= hi) want.set(c.w * 8 + c.s, c);
      let d = Math.abs(file.length - want.size);
      for (const [w, sym, n, o, h, l, c, v, nv, ps] of file) {
        const e = want.get(w * 8 + SYMBOLS.indexOf(sym));
        if (!e || e.n !== n || e.o !== o || e.h !== h || e.l !== l || e.c !== c || e.v !== v || e.nv !== nv || e.ps !== ps) d++;
      }
      byRes[res] = { candles: want.size, diffs: d };
      checked += want.size;
      diffs += d;
    }
    return { checked, diffs, byRes, ms: now() - t0, upTo };
  }

  // The quote board, read from the precompute views.
  quotes() {
    return this.db.exec({
      sql: `SELECT l.symbol, l.value AS last, o.value AS open, hi.value AS high, lo.value AS low,
              v.value AS volume, t.value AS turnover, n.value AS trades
            FROM last_price l JOIN day_open o USING (symbol) JOIN day_high hi USING (symbol, period)
              JOIN day_low lo USING (symbol, period) JOIN day_volume v USING (symbol, period)
              JOIN day_turnover t USING (symbol, period) JOIN day_trades n USING (symbol, period)
            ORDER BY l.symbol`,
      rowMode: 'object', returnValue: 'resultRows',
    });
  }

  // Candles for the chart: the whole day in 1-minute or 1-hour candles, or the last five minutes in seconds.
  candles(sym, res) {
    const lo = res === 1 ? this.eng.now() - 300 : 0;
    return this.db.exec({
      sql: `SELECT w, price_first AS o, price_max AS h, price_min AS l, price_last AS c, size_sum AS v,
              notional_sum / size_sum AS vwap, n, an FROM trades_win WHERE res = ? AND symbol = ? AND w >= ? ORDER BY w`,
      bind: [res, SYMBOLS[sym], lo], rowMode: 'object', returnValue: 'resultRows',
    });
  }

  perMinute() {
    return this.db.exec({
      // Whole minutes only: the minute still being filled would look like a sudden drop.
      sql: `SELECT w, sum(n) AS n FROM trades_win
            WHERE res = 60 AND w + 60 <= (SELECT coalesce(max(newest), 0) + 1 FROM _precomputing_sources)
            GROUP BY w ORDER BY w`,
      rowMode: 'array', returnValue: 'resultRows',
    });
  }

  alerts(limit = 8) {
    const rows = this.db.exec({
      sql: `SELECT a.ts, a.symbol, a.price, a.size, a.z,
              (SELECT price_last FROM trades_win p WHERE p.res = 1 AND p.symbol = a.symbol AND p.w < a.ts ORDER BY p.w DESC LIMIT 1) AS before1,
              (SELECT price_last FROM trades_win p WHERE p.res = 60 AND p.symbol = a.symbol AND p.w < a.ts - a.ts % 60 ORDER BY p.w DESC LIMIT 1) AS before60
            FROM trades_anomaly a ORDER BY a.rowid DESC LIMIT ?`,
      bind: [limit], rowMode: 'object', returnValue: 'resultRows',
    });
    for (const r of rows) r.before = r.before1 ?? r.before60;
    return rows;
  }

  fileBytes() {
    return this.db.selectValue('SELECT page_count * page_size FROM pragma_page_count, pragma_page_size');
  }

  snapshot(view = { sym: 0, res: 60 }) {
    const committed = this.eng.committed('feed');
    return {
      t: this.market.t, day: DAY, open: OPEN, done: this.done,
      trades: this.trades, seq: this.seq, committed, pending: this.seq - committed,
      engineMs: this.engineMs, checkpointMs: this.checkpointMs, checkpoints: this.checkpoints,
      lastCheckpoint: this.lastCheckpoint, resident: this.eng.resident(),
      fileBytes: this.fileBytes(),
      quotes: this.quotes(),
      candles: this.candles(view.sym, view.res), view,
      perMinute: this.perMinute(),
      alerts: this.alerts(),
      alertCount: this.db.selectValue('SELECT count(*) FROM trades_anomaly'),
      plugs: this.plugs,
    };
  }

  // finish checks the whole file against the recount and gathers the results.
  finish() {
    const t0 = now();
    const check = this.verify();
    const quotes = this.quotes();
    let quoteDiffs = 0;
    for (const q of quotes) {
      const e = this.expected.quote[SYMBOLS.indexOf(q.symbol)];
      if (!e || e.c !== q.last || e.o !== q.open || e.h !== q.high || e.l !== q.low || e.v !== q.volume || e.nv !== q.turnover || e.n !== q.trades) quoteDiffs++;
    }
    const total = this.db.selectValue('SELECT value FROM trades_total');
    const src = this.db.selectArray("SELECT seq, events FROM _precomputing_sources WHERE source = 'feed'");
    // The raw tier holds exactly the last five minutes of trades.
    const raw = this.db.exec({ sql: 'SELECT ts, symbol, price, size FROM trades_raw ORDER BY rowid', rowMode: 'array', returnValue: 'resultRows' });
    const cutoff = this.eng.now() - RAW_KEEP;
    const er = this.expected.raw;
    let start = 0;
    while (start < er.length && er[start] < cutoff) start += 4;
    let rawDiffs = Math.abs(raw.length - (er.length - start) / 4);
    for (let i = 0; i < raw.length && !rawDiffs; i++) {
      const j = start + 4 * i;
      if (raw[i][0] !== er[j] || raw[i][1] !== SYMBOLS[er[j + 1]] || raw[i][2] !== er[j + 2] || raw[i][3] !== er[j + 3]) rawDiffs++;
    }
    const tables = this.db.exec({
      sql: "SELECT name, sum(pgsize) AS bytes FROM dbstat WHERE name NOT LIKE 'sqlite_%' GROUP BY name ORDER BY bytes DESC",
      rowMode: 'array', returnValue: 'resultRows',
    }).map(([name, bytes]) => {
      const table = /^(trades_raw_ts|sqlite_autoindex)/.test(name) ? null : name;
      return { name, bytes, rows: table ? this.db.selectValue(`SELECT count(*) FROM "${table}"`) : null };
    });
    this.db.exec('VACUUM');
    const bytes = this.fileBytes();
    // What a plain table of every trade would take: measured on the first 100,000 trades, scaled.
    const plain = new this.sqlite3.oo1.DB(':memory:');
    plain.exec('CREATE TABLE trades (ts INTEGER, symbol TEXT, price REAL, size INTEGER)');
    const m = new Market();
    const ins = plain.prepare('INSERT INTO trades VALUES (?, ?, ?, ?)');
    const X = this.sqlite3.wasm.exports;
    let sample = 0;
    plain.exec('BEGIN');
    while (sample < 100000) {
      const o = m.second();
      for (let i = 0; i < o.n && sample < 100000; i++, sample++) {
        ins.bind(1, o.ts); ins.bind(2, SYMBOLS[o.sym[i]]);
        X.sqlite3_bind_double(ins.pointer, 3, o.price[i]); ins.bind(4, o.size[i]);
        ins.step(); ins.reset();
      }
    }
    plain.exec('COMMIT');
    ins.finalize();
    const perTrade = plain.selectValue("SELECT sum(pgsize) FROM dbstat WHERE name = 'trades'") / sample;
    plain.close();
    return {
      trades: this.trades, check, quoteDiffs, total, seq: src?.[0], events: src?.[1], rawRows: raw.length, rawDiffs,
      fileBytes: bytes, tables, plainBytes: Math.round(perTrade * this.trades), plainPerTrade: perTrade,
      engineMs: this.engineMs, checkpointMs: this.checkpointMs, checkpoints: this.checkpoints,
      maxPending: this.maxPending, plugs: this.plugs, jumps: this.jumps,
      alerts: this.alerts(50), finishMs: now() - t0,
    };
  }

  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    if (!/^(select|with|pragma|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) {
      return { error: 'This box only reads, one statement at a time. The file changes only through the Engine.' };
    }
    try {
      const columns = [];
      const rows = [];
      const t0 = now();
      this.db.exec({ sql: text, rowMode: 'array', resultRows: rows, columnNames: columns });
      const ms = now() - t0;
      return { columns, rows: rows.slice(0, 500).map((r) => r.map((v) => (typeof v === 'bigint' ? Number(v) : v))), total: rows.length, ms };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    }
  }

  exportFile() {
    return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer);
  }
}

// Race runs the first trades of the day through a fresh Engine and through the compiled triggers,
// giving each the same slices of time on the same thread, then compares the two files.
export class Race {
  constructor(sqlite3, go, policy, compiled, name = 'trades.precompute', total = RACE_TRADES) {
    this.sqlite3 = sqlite3;
    const m = new Market();
    this.seconds = [];
    let n = 0;
    while (n < total) {
      const o = m.second();
      this.seconds.push(o);
      n += o.n;
    }
    this.total = n;
    this.edb = new sqlite3.oo1.DB(':memory:');
    this.store = sqliteStore(sqlite3, this.edb);
    this.eng = go.open(this.store, policy, name);
    if (this.eng.error) throw new Error(this.eng.error);
    this.si = this.eng.stream('trades');
    this.keys = SYMBOLS.map((s) => this.eng.key(this.si, s));
    this.sdb = new sqlite3.oo1.DB(':memory:');
    this.sdb.exec(compiled.schema);
    this.distill = compiled.distill.split('\n').filter((l) => l.startsWith('DELETE'));
    this.ins = this.sdb.prepare('INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?)');
    this.e = { next: 0, trades: 0, ms: 0, done: false, seq: 0, sinceCk: 0 };
    this.q = { next: 0, at: 0, trades: 0, ms: 0, done: false, since: 0 };
  }

  // step gives each runtime sliceMs of work and returns the progress.
  step(sliceMs = 25) {
    const e = this.e, q = this.q;
    if (!e.done) {
      const t0 = now();
      while (now() - t0 < sliceMs && e.next < this.seconds.length) {
        const o = this.seconds[e.next++];
        const r = this.eng.put(this.si, 'race', e.seq + 1, o.n, new Uint8Array(pack(o, this.keys).buffer));
        if (r.error) throw new Error(r.error);
        e.seq += o.n; e.trades += o.n; e.sinceCk += o.n;
        if (e.sinceCk >= 10000) { const c = this.eng.checkpoint(); if (c.error) throw new Error(c.error); e.sinceCk = 0; }
      }
      if (e.next === this.seconds.length) {
        const c = this.eng.checkpoint();
        if (c.error) throw new Error(c.error);
        e.done = true;
      }
      e.ms += now() - t0;
    }
    if (!q.done) {
      const t0 = now();
      const X = this.sqlite3.wasm.exports;
      const ins = this.ins;
      this.sdb.exec('BEGIN');
      while (now() - t0 < sliceMs && q.next < this.seconds.length) {
        const o = this.seconds[q.next];
        const end = Math.min(o.n, q.at + 200);
        for (let i = q.at; i < end; i++) {
          ins.bind(1, o.ts); ins.bind(2, SYMBOLS[o.sym[i]]);
          X.sqlite3_bind_double(ins.pointer, 3, o.price[i]); ins.bind(4, o.size[i]);
          ins.step(); ins.reset();
        }
        q.trades += end - q.at; q.since += end - q.at;
        q.at = end;
        if (q.at === o.n) { q.next++; q.at = 0; }
        if (q.since >= 10000) { for (const d of this.distill) this.sdb.exec({ sql: d, bind: { ':now': o.ts } }); q.since = 0; }
      }
      if (q.next === this.seconds.length) {
        const last = this.seconds[this.seconds.length - 1].ts;
        for (const d of this.distill) this.sdb.exec({ sql: d, bind: { ':now': last } });
        q.done = true;
      }
      this.sdb.exec('COMMIT');
      q.ms += now() - t0;
    }
    return this.progress();
  }

  progress() {
    return {
      total: this.total,
      engine: { trades: this.e.trades, ms: this.e.ms, done: this.e.done },
      sql: { trades: this.q.trades, ms: this.q.ms, done: this.q.done },
    };
  }

  get done() { return this.e.done && this.q.done; }

  // compare reads every table of both files and compares them value by value, to the last bit.
  compare() {
    const f = new Float64Array(2), u = new BigUint64Array(f.buffer);
    const same = (a, b) => {
      if (typeof a === 'number' && typeof b === 'number') { f[0] = a; f[1] = b; return u[0] === u[1]; }
      return a === b;
    };
    const tables = this.sdb.selectValues("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name");
    const out = [];
    for (const name of tables) {
      const sql = this.sdb.selectValue('SELECT sql FROM sqlite_master WHERE name = ?', name);
      const st = this.sdb.prepare(`SELECT * FROM ${name}`);
      const cols = st.columnCount;
      st.finalize();
      const order = /WITHOUT ROWID/.test(sql) ? Array.from({ length: cols }, (_, i) => i + 1).join(', ') : 'rowid';
      const q = `SELECT * FROM ${name} ORDER BY ${order}`;
      const a = this.sdb.exec({ sql: q, rowMode: 'array', returnValue: 'resultRows' });
      const b = this.edb.exec({ sql: q, rowMode: 'array', returnValue: 'resultRows' });
      let diffs = Math.abs(a.length - b.length), values = 0;
      if (!diffs) for (let i = 0; i < a.length; i++) for (let j = 0; j < a[i].length; j++) { values++; if (!same(a[i][j], b[i][j])) diffs++; }
      out.push({ name, rows: a.length, values, diffs });
    }
    return out;
  }

  close() {
    this.ins.finalize();
    this.eng.close();
    this.store.close();
    this.edb.close();
    this.sdb.close();
  }
}
