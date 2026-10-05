// Demo 5: the changes to every Wikimedia wiki, as they happen, through the Engine (Go, built for
// the browser), which keeps its file in SQLite's WebAssembly build. The page reads the answers
// from the file; a recount kept apart from the Engine checks every count in it. Runs in the page's
// worker and, unchanged, in Node (tools/run-demo5.mjs).
import { startGo, sqliteStore } from '../../lib/engine.js';
import { readChange, isArticle } from './wiki.js';

export const NAME = 'wikipedia.precompute';
export const TRENDING = 600;           // trending looks at about the last ten minutes
export const CHART = 1800;             // the chart shows the last half hour
const SAFE = 3500;                     // windows kept for an hour are checked while younger than this
const SEEN = 50000;                    // event ids remembered, to drop an event sent twice
const now = () => performance.now();

// Filters on the page: every wiki, every Wikipedia, or one wiki.
export function where(filter, col = 'wiki') {
  if (!filter || filter === 'all') return { sql: '1', args: [] };
  if (filter === 'wikipedias') return { sql: `${col} LIKE '%.wikipedia.org'`, args: [] };
  return { sql: `${col} = ?`, args: [filter] };
}

// Recount keeps every count and sum the file should hold, straight from the events, window by
// window: stream -> "res w" -> key -> {n, bytes}. It never reads the file.
class Recount {
  constructor() { this.m = { edits: new Map(), article_edits: new Map(), article_editors: new Map() }; }
  add(stream, res, w, k, bytes) {
    const m = this.m[stream];
    const wk = `${res} ${w}`;
    let win = m.get(wk);
    if (!win) m.set(wk, (win = new Map()));
    const x = win.get(k);
    if (x) { x.n++; x.bytes += bytes; } else win.set(k, { n: 1, bytes });
  }
  // prune forgets windows the file lets go of, well after it does.
  prune(t) {
    for (const m of Object.values(this.m)) for (const wk of m.keys()) {
      const [res, w] = wk.split(' ').map(Number);
      if (res <= 60 && w < t - 4000 && (res === 10 || m !== this.m.edits)) m.delete(wk);
    }
  }
}

export class Run {
  // module: the compiled Go program (loadGo); policy: the policy's text.
  constructor(sqlite3, module, policy) {
    this.sqlite3 = sqlite3;
    this.module = module;
    this.policy = policy;
    this.db = null;
  }

  async init() {
    this.go = await startGo(this.module);
    this.compiled = this.go.compile(this.policy, NAME);
    if (this.compiled.error) throw new Error(this.compiled.error);
    this.reset();
    return this;
  }

  close() {
    if (this.eng) this.eng.close();
    if (this.store) this.store.close();
    for (const st of this.stmts?.values() || []) st.finalize();
    if (this.db) this.db.close();
    this.eng = this.store = this.db = null;
    this.stmts = new Map();
  }

  reset() {
    this.close();
    this.db = new this.sqlite3.oo1.DB(':memory:');
    this.store = sqliteStore(this.sqlite3, this.db);
    this.eng = this.go.open(this.store, this.policy, NAME);
    if (this.eng.error) throw new Error(this.eng.error);
    this.pageSize = this.db.selectValue('PRAGMA page_size');
    this.streams = ['edits', 'article_edits', 'article_editors'].map((name) => ({ name, i: this.eng.stream(name), keys: new Map(), rows: [], seq: 0 }));
    this.recount = new Recount();
    this.seen = new Set();
    this.seenOrder = [];
    this.counts = { events: 0, canaries: 0, repeats: 0, unread: 0, articles: 0, people: 0 };
    this.first = null;
    this.engineMs = 0;
    this.lastCheck = null;
    this.checked = null;
    this.onEvent = null;                 // a test can see every event as the Engine gets it
  }

  keyId(s, parts) {
    const k = parts.join('\u0000');
    let id = s.keys.get(k);
    if (id === undefined) {
      id = this.eng.key(s.i, ...parts);
      if (id < 0) throw new Error(this.eng.keyError || 'key refused');
      s.keys.set(k, id);
    }
    return id;
  }

  // add takes one event of the stream, as an object. It returns what became of it.
  add(e) {
    const c = readChange(e);
    if (!c) { this.counts.unread++; return 'unread'; }
    if (c.canary) { this.counts.canaries++; return 'canary'; }
    if (c.id) {
      if (this.seen.has(c.id)) { this.counts.repeats++; return 'repeat'; }
      this.seen.add(c.id);
      this.seenOrder.push(c.id);
      if (this.seenOrder.length > SEEN) this.seen.delete(this.seenOrder.shift());
    }
    this.counts.events++;
    if (this.first == null || c.ts < this.first) this.first = c.ts;
    const who = c.bot ? 'bot' : 'person';
    const [edits, articles, editors] = this.streams;
    const rc = this.recount;
    this.put(edits, c.ts, [c.wiki, c.kind, who], c.bytes);
    for (const res of [10, 60, 3600]) rc.add('edits', res, c.ts - (c.ts % res), `${c.wiki}\u0000${c.kind}\u0000${who}`, c.bytes);
    if (isArticle(c)) {
      const m = c.ts - (c.ts % 60);
      this.counts.articles++;
      this.put(articles, c.ts, [c.wiki, c.title], c.bytes);
      rc.add('article_edits', 60, m, `${c.wiki}\u0000${c.title}`, c.bytes);
      if (!c.bot) {
        this.counts.people++;
        this.put(editors, c.ts, [c.wiki, c.title, c.editor], c.bytes);
        rc.add('article_editors', 60, m, `${c.wiki}\u0000${c.title}\u0000${c.editor}`, c.bytes);
      }
    }
    return 'counted';
  }

  put(s, ts, key, bytes) {
    s.rows.push(ts, this.keyId(s, key), bytes);
    if (this.onEvent) this.onEvent(s.name, ts, key, bytes);
  }

  // checkpoint hands the events so far to the Engine and has it write the file.
  checkpoint() {
    const t0 = now();
    for (const s of this.streams) {
      if (!s.rows.length) continue;
      const n = s.rows.length / 3;
      const rows = new Float64Array(s.rows);
      const r = this.eng.put(s.i, s.name, s.seq + 1, n, new Uint8Array(rows.buffer));
      if (r.error) throw new Error(r.error);
      s.seq += n;
      s.rows = [];
    }
    const c = this.eng.checkpoint();
    this.engineMs += now() - t0;
    if (c.error) throw new Error(c.error);
    return c;
  }

  get t() { return this.eng.now(); }

  rows(sql, args) {
    let st = this.stmts.get(sql);
    if (!st) { st = this.db.prepare(sql); this.stmts.set(sql, st); }
    if (args && args.length) st.bind(args);
    const out = [];
    while (st.step()) out.push(st.get([]));
    st.reset();
    return out;
  }

  // check compares every count and sum in the file with the recount: every window the file keeps
  // (windows kept for an hour while younger than 58 minutes), or with since, only the windows
  // from then on, which is all that can have changed since the last check.
  check(since = null) {
    const t = this.t;
    if (!t) return null;
    const t0 = now();
    const from = since == null ? -Infinity : since;
    let cells = 0, same = 0, first = null;
    const specs = [
      ['edits', 'SELECT res, w, wiki, kind, who, n, bytes_sum FROM edits_win WHERE w >= ? - res', 3],
      ['article_edits', 'SELECT res, w, wiki, title, n, bytes_sum FROM article_edits_win WHERE res = 60 AND w >= ? - 60', 2],
      ['article_editors', 'SELECT res, w, wiki, title, editor, n, bytes_sum FROM article_editors_win WHERE res = 60 AND w >= ? - 60', 3],
    ];
    for (const [stream, sql, nk] of specs) {
      const hourly = stream === 'edits';
      const inside = (res, w) => (hourly && res >= 60) || w > t - SAFE;
      const got = new Map();
      for (const r of this.rows(sql, [since == null ? -1e12 : from])) {
        const [res, w] = r;
        if (!inside(res, w) || w + res <= from) continue;
        const wk = `${res} ${w}`;
        let win = got.get(wk);
        if (!win) got.set(wk, (win = new Map()));
        win.set(r.slice(2, 2 + nk).join('\u0000'), { n: r[2 + nk], bytes: r[3 + nk] });
      }
      const want = this.recount.m[stream];
      const keys = new Set(got.keys());
      for (const wk of want.keys()) {
        const [res, w] = wk.split(' ').map(Number);
        if (inside(res, w) && w + res > from) keys.add(wk);
      }
      for (const wk of keys) {
        const a = want.get(wk) || new Map(), b = got.get(wk) || new Map();
        for (const k of new Set([...a.keys(), ...b.keys()])) {
          cells++;
          const x = a.get(k), y = b.get(k);
          if (x && y && x.n === y.n && x.bytes === y.bytes) same++;
          else if (!first) first = `${stream} ${wk} ${k.replace(/\u0000/g, ' | ')}: recount ${x ? `${x.n}, ${x.bytes}` : 'none'}, file ${y ? `${y.n}, ${y.bytes}` : 'none'}`;
        }
      }
    }
    const total = this.rows('SELECT value FROM changes')[0]?.[0] ?? 0;
    cells++;
    if (total === this.counts.events) same++;
    else if (!first) first = `changes: recount ${this.counts.events}, file ${total}`;
    this.recount.prune(t);
    const c = { at: t, cells, same, first, ms: now() - t0, full: since == null };
    this.checked = { cells: (this.checked?.cells || 0) + cells, same: (this.checked?.same || 0) + same, first: this.checked?.first || first, checks: (this.checked?.checks || 0) + 1 };
    this.lastCheck = c;
    return c;
  }

  // The answers the page shows, read from the file.
  snapshot(filter = 'all') {
    const t = this.t;
    const out = { t, first: this.first, counts: { ...this.counts }, engineMs: this.engineMs, check: this.lastCheck, checked: this.checked,
      fileBytes: (this.db.selectValue('PRAGMA page_count') - this.db.selectValue('PRAGMA freelist_count')) * this.pageSize };
    if (!t) return { ...out, chart: [], trending: [], wikis: [], kinds: [], rate: 0, bots: 0, total: 0 };
    const q0 = now();
    const f = where(filter);
    const since = t - (t % 60) - (TRENDING - 60);
    out.total = this.rows('SELECT value FROM changes')[0]?.[0] ?? 0;
    // The chart leaves out the first window, which began before the first change arrived.
    const firstFull = this.first - (this.first % 10) + 10;
    out.chart = this.rows(`SELECT w, sum(CASE WHEN who = 'person' THEN n ELSE 0 END), sum(CASE WHEN who = 'bot' THEN n ELSE 0 END)
      FROM edits_win WHERE res = 10 AND w >= ? AND ${f.sql} GROUP BY w ORDER BY w`, [Math.max(t - CHART, firstFull), ...f.args]);
    // The rate: every change in the last minute of whole 10-second windows, or in as much of it as there is.
    const full = t - (t % 10);
    const from = Math.max(full - 60, firstFull);
    const last = this.rows('SELECT sum(n) FROM edits_win WHERE res = 10 AND w >= ? AND w < ?', [from, full])[0][0] || 0;
    out.rate = full > from ? last / (full - from) : 0;
    out.trending = this.rows(`SELECT wiki, title, count(DISTINCT editor) AS people, sum(n) AS edits, sum(bytes_sum) AS bytes
      FROM article_editors_win WHERE res = 60 AND w >= ? AND ${f.sql}
      GROUP BY wiki, title HAVING people >= 2 ORDER BY people DESC, edits DESC, title LIMIT 10`, [since, ...f.args]);
    out.wikis = this.rows(`SELECT wiki, sum(n), sum(CASE WHEN who = 'bot' THEN n ELSE 0 END), sum(bytes_sum)
      FROM edits_win WHERE res = 60 AND w >= ? GROUP BY wiki ORDER BY 2 DESC, wiki LIMIT 12`, [since]);
    out.kinds = this.rows(`SELECT kind, sum(n), sum(CASE WHEN who = 'bot' THEN n ELSE 0 END)
      FROM edits_win WHERE res = 60 AND w >= ? AND ${f.sql} GROUP BY kind ORDER BY 2 DESC`, [since, ...f.args]);
    const [all, bots] = this.rows("SELECT sum(n), sum(CASE WHEN who = 'bot' THEN n ELSE 0 END) FROM edits_win WHERE res = 60 AND w >= ?", [since])[0];
    out.bots = all ? bots / all : 0;
    out.recent = all || 0;
    out.since = since;
    out.readMs = now() - q0;
    return out;
  }

  // The wikis seen so far, busiest first, for the filter.
  wikiList() {
    return this.rows("SELECT wiki, sum(n) FROM edits_win WHERE res = 3600 GROUP BY wiki ORDER BY 2 DESC, wiki").map(([w, n]) => ({ wiki: w, n }));
  }

  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    const refused = { error: 'This box only reads, one statement at a time. The file changes only through its streams.' };
    if (!/^(select|with|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) return refused;
    let stmt = null;
    try {
      const t0 = now();
      stmt = this.db.prepare(text);
      // SQLite's own test, so that a statement such as WITH ... DELETE is refused too.
      if (!stmt.isReadOnly()) return refused;
      const columns = stmt.getColumnNames();
      const rows = [];
      while (stmt.step()) rows.push(stmt.get([]));
      return { columns, rows: rows.slice(0, 500), total: rows.length, ms: now() - t0 };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    } finally {
      if (stmt) stmt.finalize();
    }
  }

  tables() {
    return this.db.selectArrays('SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY name')
      .filter(([n]) => !n.startsWith('sqlite_'))
      .map(([name, bytes]) => ({ name, bytes, rows: this.db.selectValue("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", [name]) ? this.db.selectValue(`SELECT count(*) FROM "${name}"`) : null }));
  }

  exportFile() { return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer); }
}
