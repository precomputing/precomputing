// One run of Demo 4: two hours of a web shop's logs through the log reducer and the Engine (Go,
// built for the browser), which keep their file in SQLite's WebAssembly build. Every minute the
// answers the dashboard needs, error lines, examples, unusual requests and new templates go
// upstream; the raw lines stay in the file. Runs in the page's worker and, unchanged, in Node,
// which is how the published numbers are made (tools/run-demo4.mjs).
import { START, DURATION, Shop, INCIDENT, DEPLOY } from './scenario.js';
import { startGo, sqliteStore } from '../../lib/engine.js';

export const CHECKPOINT = 5;           // simulated seconds between checkpoints
export const WARMUP = 300;             // templates first seen in the first five minutes are being learned, not news
export const ALERT_LEVELS = ['WARN', 'ERROR'];
export const TAIL = 14;                // raw lines shown on the page
const enc = new TextEncoder();
const now = () => performance.now();

const LINE_RE = /^(\S+)\s+(\S+)\s+(\S+)\s+(.*)$/;
const KV_RE = /(^|\s)([A-Za-z_][A-Za-z0-9_.]*)=("[^"]*"|\S*)/g;
const PCT_RE = /^p(\d{1,2}|999)$/;

export function quantileOf(measure) {
  const m = PCT_RE.exec(measure);
  if (!m) return null;
  return m[1] === '999' ? 0.999 : Number(m[1]) / 100;
}

// Recount reads the raw lines the way a log search tool would, and computes every panel from them
// minute by minute. It never reads the Engine's file.
class Recount {
  constructor(plans) {
    this.plans = plans.map((p) => ({ ...p, q: quantileOf(p.measure), data: new Map() }));
  }

  add(line) {
    const m = LINE_RE.exec(line);
    if (!m) return;
    const ts = Math.floor(Date.parse(m[1]) / 1000);
    if (!Number.isFinite(ts)) return;
    const f = { timestamp: m[1], level: m[2], service: m[3] };
    for (const kv of m[4].matchAll(KV_RE)) {
      let v = kv[3];
      if (v.length >= 2 && v[0] === '"' && v[v.length - 1] === '"') v = v.slice(1, -1);
      if (!(kv[2] in f)) f[kv[2]] = v;
    }
    const w = ts - (ts % 60);
    for (const p of this.plans) {
      let ok = true;
      for (const k in p.where) if (f[k] !== p.where[k]) { ok = false; break; }
      if (!ok || p.by.some((b) => !(b in f))) continue;
      let x = 0;
      if (p.field) {
        if (!(p.field in f)) continue;
        x = Number(f[p.field]);
        if (!Number.isFinite(x) || f[p.field] === '') continue;
      }
      const key = p.by.map((b) => f[b]).join('\u0000');
      let mm = p.data.get(w);
      if (!mm) p.data.set(w, (mm = new Map()));
      const acc = mm.get(key);
      switch (p.measure) {
        case 'count': case 'rate': mm.set(key, (acc || 0) + 1); break;
        case 'sum': mm.set(key, acc === undefined ? x : acc + x); break;
        case 'avg': if (acc) { acc.s += x; acc.n++; } else mm.set(key, { s: x, n: 1 }); break;
        case 'min': mm.set(key, acc === undefined || x < acc ? x : acc); break;
        case 'max': mm.set(key, acc === undefined || x > acc ? x : acc); break;
        default: if (acc) acc.push(x); else mm.set(key, [x]);
      }
    }
  }

  // value is a panel's exact answer for one key in one minute.
  value(p, acc) {
    if (p.measure === 'avg') return acc.s / acc.n;
    if (p.q != null) {
      const xs = acc.slice().sort((a, b) => a - b);
      return xs[Math.max(0, Math.ceil(p.q * xs.length) - 1)];
    }
    return acc;
  }
}

export class Run {
  // module: the compiled Go program (loadGo); files: {policy, dashboard} texts.
  constructor(sqlite3, module, files) {
    this.sqlite3 = sqlite3;
    this.module = module;
    this.files = files;
    this.db = null;
  }

  async init() {
    this.go = await startGo(this.module);
    this.compiled = this.go.compile(this.files.policy, 'shop.precompute');
    if (this.compiled.error) throw new Error(this.compiled.error);
    const imp = this.go.importDashboard(this.files.dashboard);
    if (imp.error) throw new Error(imp.error);
    this.plans = imp.plans.map((p, i) => ({ i, title: p.title, type: p.type || 'timeseries', where: p.where || {}, measure: p.measure,
      field: p.field || '', by: p.by || [], top: p.top || 10, stream: p.stream }));
    this.reset();
    return this;
  }

  reset() {
    if (this.eng) this.eng.close();
    if (this.store) this.store.close();
    for (const st of this.stmts?.values() || []) st.finalize();
    if (this.db) this.db.close();
    this.db = new this.sqlite3.oo1.DB(':memory:');
    this.store = sqliteStore(this.sqlite3, this.db);
    this.eng = this.go.logs(this.store, this.files.policy, 'shop.precompute');
    if (this.eng.error) throw new Error(this.eng.error);
    this.stmts = new Map();
    this.pageSize = this.db.selectValue('PRAGMA page_size');
    // The sketch constant of each percentile panel, from what the file says about itself.
    for (const p of this.plans) {
      if (quantileOf(p.measure) == null) continue;
      const d = JSON.parse(this.db.selectValue("SELECT detail FROM _precomputing_objects WHERE name = ?", [`${p.stream}_${p.field}_sk`]));
      p.gamma = d.gamma;
    }
    this.anomalyTables = this.db.selectValues("SELECT name FROM _precomputing_objects WHERE kind = 'anomalies'");
    this.shop = new Shop();
    this.recount = new Recount(this.plans);
    this.received = this.plans.map(() => new Map());   // what upstream holds: per panel, minute -> key -> value
    this.seq = 0;
    this.lines = 0;
    this.rawBytes = 0;
    this.sentBytes = 0;
    this.sentEvents = 0;
    this.batches = 0;
    this.engineMs = 0;
    this.tail = [];
    this.alerts = [];
    this.pending = [];            // new templates not yet sent
    this.dictionary = 0;          // templates sent upstream
    this.minutesOut = [];         // minutes not yet handed to the page
    this.checks = this.plans.map(() => ({ points: 0, same: 0, worst: 0 }));
    this.shards = [];
    this.news = [];
    this.done = false;
    this.finished = null;
  }

  close() {
    if (this.eng) this.eng.close();
    if (this.store) this.store.close();
    for (const st of this.stmts?.values() || []) st.finalize();
    if (this.db) this.db.close();
    this.eng = this.store = this.db = null;
  }

  get t() { return this.shop.t; }

  setRate(r) { this.shop.rate = r; }

  breakShard() {
    const s = this.shop.breakShard();
    this.shards.push(s);
    this.news.push({ t: s.from, kind: 'red', text: 'You broke a search index shard for three minutes: search starts logging an error with every query.' });
    return s;
  }

  // Simulated seconds until maxSeconds are done or budgetMs has passed.
  step(maxSeconds, budgetMs = 60) {
    if (this.done) return 0;
    const t0 = now();
    let secs = 0;
    while (secs < maxSeconds && now() - t0 < budgetMs) {
      const t = this.shop.t;
      const lines = this.shop.second();
      if (!lines) { this.done = true; break; }
      for (const l of lines) {
        this.rawBytes += l.length + 1;
        this.recount.add(l);
      }
      this.lines += lines.length;
      this.tail.push(...lines);
      if (this.tail.length > 4 * TAIL) this.tail = this.tail.slice(-TAIL);
      if (lines.length) {
        const e0 = now();
        const res = this.eng.putLines(lines.join('\n'), 'shop', this.seq + 1);
        this.engineMs += now() - e0;
        this.seq += lines.length;
        if (res.error) throw new Error(res.error);
        for (const nt of res.newTemplates || []) this.pending.push(nt);
      }
      secs++;
      const next = t + 1;
      if (next % CHECKPOINT === 0 || next >= DURATION) this.checkpoint(next);
      if (next % 60 === 0) this.minute(START + next - 60);
      if (next >= DURATION) { this.done = true; break; }
    }
    return secs;
  }

  checkpoint(t) {
    const e0 = now();
    const r = this.eng.checkpoint();
    this.engineMs += now() - e0;
    if (r.error) throw new Error(r.error);
    if (!this.pending.length) return;
    // New templates go upstream at once; one at WARN or ERROR after the first minutes is an alert.
    const msg = { at: START + t, templates: this.pending.map((x) => [x.id, x.service, x.level, x.text, x.example]) };
    this.send(msg, this.pending.length);
    this.dictionary += this.pending.length;
    for (const x of this.pending) {
      const first = x.firstTs - START;
      if (ALERT_LEVELS.includes(x.level) && first >= WARMUP) {
        const a = { ...x, first, sent: t, caughtAfter: t - first };
        this.alerts.push(a);
        this.news.push({ t: first, kind: 'red', text: `Alert, ${t - first} s after its first line: a new ${x.level} line in ${x.service}, “${x.text}”.` });
      } else if (first >= WARMUP) {
        this.news.push({ t: first, kind: 'amber', text: `A new ${x.level} line in ${x.service}: “${x.text}”. Counted as a template of its own; not an alert.` });
      }
    }
    this.pending = [];
  }

  send(obj, events) {
    this.sentBytes += enc.encode(JSON.stringify(obj)).length;
    this.sentEvents += events;
    this.batches++;
  }

  rows(sql, args) {
    let st = this.stmts.get(sql);
    if (!st) { st = this.db.prepare(sql); this.stmts.set(sql, st); }
    st.bind(args);
    const out = [];
    while (st.step()) out.push(st.get([]));
    st.reset();
    return out;
  }

  // panelRows reads one panel's answers for one minute from the file: its keys, then the value.
  panelRows(p, w) {
    const keys = p.by.join(', ');
    const sel = keys ? keys + ', ' : '';
    const q = quantileOf(p.measure);
    if (q != null) {
      const g = p.gamma;
      const part = keys ? `PARTITION BY ${keys}` : '';
      return this.rows(`WITH c AS (SELECT ${sel}b,
          sum(n) OVER (${part} ORDER BY b ROWS UNBOUNDED PRECEDING) AS cum, sum(n) OVER (${part}) AS tot
        FROM ${p.stream}_${p.field}_sk WHERE res = 60 AND w = ?)
        SELECT ${sel}2 * pow(${g}, min(b)) / (${g} + 1) FROM c WHERE cum >= ${q} * tot${keys ? ' GROUP BY ' + keys : ''}`, [w]);
    }
    const col = { count: 'n', rate: 'n', sum: `${p.field}_sum`, avg: `${p.field}_sum / n`, min: `${p.field}_min`, max: `${p.field}_max` }[p.measure];
    return this.rows(`SELECT ${sel}${col} FROM ${p.stream}_win WHERE res = 60 AND w = ?`, [w]);
  }

  // minute sends upstream what the minute that just ended adds, and checks it against the recount.
  minute(w) {
    const batch = { w, panels: [], errors: [], anomalies: [], samples: [] };
    let events = 0;
    for (const p of this.plans) {
      const rows = this.panelRows(p, w);
      batch.panels.push(rows.map((r) => r.map((x) => (typeof x === 'number' && !Number.isInteger(x) ? Number(x.toPrecision(10)) : x))));
      events += rows.length;
    }
    batch.errors = this.rows('SELECT ts, line FROM errors_sample WHERE res = 60 AND w = ?', [w]);
    for (const tbl of this.anomalyTables) {
      batch.anomalies.push(...this.rows(`SELECT ts, round(z, 1), line FROM ${tbl} WHERE ts >= ? AND ts < ?`, [w, w + 60]));
    }
    if ((w + 60 - START) % 600 === 0) batch.samples = this.rows('SELECT ts, line FROM lines_sample WHERE res = 600 AND w = ?', [w + 60 - 600]);
    events += batch.errors.length + batch.anomalies.length + batch.samples.length;
    this.send(batch, events);
    // Upstream keeps what it was sent; the panels there are drawn from it alone.
    const got = [];
    for (const [i, p] of this.plans.entries()) {
      const mm = new Map();
      for (const r of batch.panels[i]) mm.set(r.slice(0, p.by.length).join('\u0000'), r[p.by.length]);
      this.received[i].set(w, mm);
      got.push(this.check(p, w, mm));
    }
    this.minutesOut.push({ w, panels: batch.panels, checks: got, errors: batch.errors.length, anomalies: batch.anomalies.length, bytes: this.sentBytes, raw: this.rawBytes });
  }

  // check compares one minute of a panel as sent with the recount of the raw lines.
  check(p, w, got) {
    const rp = this.recount.plans[p.i];
    const want = rp.data.get(w) || new Map();
    const c = this.checks[p.i];
    let same = true;
    const keys = new Set([...want.keys(), ...got.keys()]);
    for (const k of keys) {
      c.points++;
      const a = got.get(k), acc = want.get(k);
      if (a === undefined || acc === undefined) { same = false; continue; }
      const e = this.recount.value(rp, acc);
      if (rp.q != null) {
        const err = Math.abs(a - e) / e;
        if (err > c.worst) c.worst = err;
        if (err <= 0.0100001) c.same++; else same = false;
      } else if (p.measure === 'sum' || p.measure === 'avg') {
        // Sums travel as 10 significant digits; the file holds the exact double.
        if (Math.abs(a - e) <= Math.abs(e) * 1e-9) c.same++; else same = false;
      } else if (a === e) c.same++;
      else same = false;
    }
    return same;
  }

  bytes(db = this.db) {
    return (db.selectValue('PRAGMA page_count') - db.selectValue('PRAGMA freelist_count')) * this.pageSize;
  }

  // The panels as upstream holds them, for the minutes not yet on the page.
  snapshot() {
    const minutes = this.minutesOut;
    this.minutesOut = [];
    const news = this.news;
    this.news = [];
    const tpl = this.eng.templates() || [];
    return {
      t: this.shop.t,
      duration: DURATION,
      rate: this.shop.rate,
      lines: this.lines,
      rawBytes: this.rawBytes,
      sentBytes: this.sentBytes,
      sentEvents: this.sentEvents,
      batches: this.batches,
      engineMs: this.engineMs,
      fileBytes: this.bytes(),
      templates: tpl.map((x) => ({ id: x.id, service: x.service, level: x.level, text: x.text, n: x.n, firstTs: x.firstTs, example: x.example })),
      tail: this.tail.slice(-TAIL),
      minutes,
      checks: this.checks,
      alerts: this.alerts,
      shards: this.shards,
      news,
      done: this.done,
    };
  }

  // The top rows of a toplist panel over the whole run: as sent, and from the raw lines.
  top(p) {
    const sum = (maps, val) => {
      const tot = new Map();
      for (const mm of maps.values()) for (const [k, v] of mm) tot.set(k, (tot.get(k) || 0) + val(v));
      return [...tot].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, p.top);
    };
    return { sent: sum(this.received[p.i], (v) => v), raw: sum(this.recount.plans[p.i].data, (v) => v) };
  }

  search(text, limit = 100) {
    const t0 = now();
    const like = `%${text}%`;
    const n = this.db.selectValue('SELECT count(*) FROM lines_raw WHERE line LIKE ?', [like]);
    const rows = this.db.selectArrays('SELECT ts, line FROM lines_raw WHERE line LIKE ? ORDER BY ts DESC, rowid DESC LIMIT ?', [like, limit]);
    return { text, n, rows, ms: now() - t0 };
  }

  finish() {
    if (this.finished) return this.finished;
    while (!this.done) this.step(1e9, 1e9);
    const tables = this.db.selectArrays('SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY name')
      .filter(([n]) => !n.startsWith('sqlite_'))
      .map(([name, bytes]) => ({ name, bytes, rows: this.db.selectValue(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, [name]) ? this.db.selectValue(`SELECT count(*) FROM "${name}"`) : null }));
    const tops = this.plans.filter((p) => p.type === 'toplist').map((p) => ({ i: p.i, ...this.top(p) }));
    const search = this.search(INCIDENT.provider);
    this.finished = {
      lines: this.lines,
      rawBytes: this.rawBytes,
      sentBytes: this.sentBytes,
      sentEvents: this.sentEvents,
      batches: this.batches,
      engineMs: this.engineMs,
      fileBytes: this.bytes(),
      rawRows: this.db.selectValue('SELECT count(*) FROM lines_raw'),
      templates: (this.eng.templates() || []).length,
      checks: this.checks,
      tops,
      alerts: this.alerts,
      search: { text: search.text, n: search.n, ms: search.ms },
      tables,
      incident: INCIDENT,
      deploy: DEPLOY,
      sqlite: this.sqlite3.version.libVersion,
    };
    return this.finished;
  }

  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    if (!/^(select|with|pragma|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) {
      return { error: 'This box only reads, one statement at a time. The file changes only through its streams.' };
    }
    try {
      const columns = [];
      const rows = [];
      const t0 = now();
      this.db.exec({ sql: text, rowMode: 'array', resultRows: rows, columnNames: columns });
      return { columns, rows: rows.slice(0, 500), total: rows.length, ms: now() - t0 };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    }
  }

  importDashboard(text) { return this.go.importDashboard(text); }

  exportFile() { return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer); }
}
