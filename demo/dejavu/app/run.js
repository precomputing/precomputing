// One run of Demo 6: two hours of the web shop's logs, today, through the log reducer and the
// Engine, on the file that already holds the last 24 hours and the incidents the on-call team wrote
// down. Every ten seconds the run asks the file what stands out and which incident it looks like
// (the views in dejavu.sql). Runs in the page's worker and, unchanged, in Node, which is how the
// published numbers are made (tools/run-demo6.mjs).
import { TODAY_START, TODAY_DURATION, TODAY_INCIDENTS, TODAY_LABEL, HISTORY_INCIDENTS, KINDS, Shop } from './scenario.js';
import { startGo, sqliteStore } from '../../lib/engine.js';
import { NAME, openFile } from './history.js';

export const CHECK_EVERY = 10;     // simulated seconds between checks
export const MATCH = 0.7;          // at least this similar: it looks like that incident
export const PARTLY = 0.4;         // at least this similar: partly like it
export const TIE = 0.05;           // incidents this close to the closest are told apart by their raw lines
export const QUIET_AFTER = 60;     // an episode ends after this long with nothing standing out
export const GRACE = 120;          // how long an incident's lines stay in the last minute or two after it ends
export const FINGERPRINT = 120;    // a written-down incident's fingerprint is its first two minutes
export const TAIL = 8;             // warnings and errors shown on the page
export const TODAY_SEED = 930;
const now = () => performance.now();
const LINE_RE = /^\S+\s+(\S+)\s+(\S+)\s+/;

// kindKey is what makes two incidents the same kind: the kind, and for payments the provider.
export const kindKey = (x) => (x.kind === 'payments' ? `payments:${x.provider || 'northpay'}` : x.kind);

// What the page says when an incident begins and ends.
export function begins(x) {
  switch (x.kind) {
    case 'payments': return `Card payments start failing at ${x.provider}.`;
    case 'attack': return 'A login attack begins: failed sign-ins from hundreds of addresses.';
    case 'search': return 'Search shard 3 loses its replica.';
    case 'dbpool': return 'The checkout database runs out of connections.';
    case 'cache': return 'The recommendations cache goes down.';
    case 'tls': return `The certificate on ${x.host} expires, and product images start failing.`;
    case 'queue': return 'The order queue runs out of disk.';
    default: return `${KINDS[x.kind].name} begins.`;
  }
}
export function ends(x) {
  switch (x.kind) {
    case 'payments': return `${x.provider} recovers.`;
    case 'attack': return 'The attack stops.';
    case 'search': return 'Shard 3 has its replica again.';
    case 'dbpool': return 'The checkout database recovers.';
    case 'cache': return 'The cache is back.';
    case 'tls': return `The certificate on ${x.host} is renewed.`;
    case 'queue': return 'The order queue has disk again.';
    default: return `${KINDS[x.kind].name} is over.`;
  }
}

// hiddenWords returns the words of a raw line where its template has <*>: what the counts leave out.
export function hiddenWords(template, line) {
  if (!template || !line) return [];
  const tw = template.split(' ');
  const lw = line.replace(/^\S+\s+\S+\s+\S+\s+/, '').split(' ');
  if (tw.length !== lw.length) return [];
  const out = [];
  for (let i = 0; i < tw.length; i++) if (tw[i].includes('<*>')) out.push(lw[i]);
  return out;
}

const FP_SQL = `SELECT f.service, f.level, f.template, f.now_rate, f.usual_rate, f.score, t.template
FROM fingerprint_now f LEFT JOIN _precomputing_templates t ON t.id = f.template ORDER BY f.score DESC`;
const DV_SQL = 'SELECT id, title, fix, started, source, similarity, shared, keys FROM deja_vu LIMIT 6';
const WORDS_SQL = `SELECT k.service, k.level, k.template, t.template, k.example,
  (SELECT r.line FROM lines_raw r WHERE r.service = k.service AND r.level = k.level AND r.template = k.template
   ORDER BY r.ts DESC, r.rowid DESC LIMIT 1)
FROM incident_keys k JOIN _precomputing_templates t ON t.id = k.template
WHERE k.incident = ? AND k.example IS NOT NULL`;

export class Run {
  // module: the compiled Go program (loadGo); history: the bytes of the file the day starts from.
  // opts.seed: the shop's seed today; opts.script: false leaves out today's scripted incidents and label.
  constructor(sqlite3, module, history, opts = {}) {
    this.sqlite3 = sqlite3;
    this.module = module;
    this.history = history;
    this.opts = { seed: TODAY_SEED, script: true, ...opts };
    this.db = null;
  }

  async init() {
    this.go = await startGo(this.module);
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
    this.db = openFile(this.sqlite3, this.history);
    this.store = sqliteStore(this.sqlite3, this.db);
    this.eng = this.go.logs(this.store, null, NAME);
    if (this.eng.error) throw new Error(this.eng.error);
    this.pageSize = this.db.selectValue('PRAGMA page_size');
    this.seq = this.db.selectValue("SELECT seq FROM _precomputing_sources WHERE source = 'shop'") || 0;
    this.historyLines = this.seq;
    const script = this.opts.script;
    this.shop = new Shop({ start: TODAY_START, seconds: TODAY_DURATION, seed: this.opts.seed,
      incidents: script ? TODAY_INCIDENTS.map((x) => ({ ...x, at: TODAY_START + x.at })) : [] });
    this.truth = [];
    for (const x of this.shop.incidents) this.addTruth(x, 'script');
    this.kindOf = new Map(HISTORY_INCIDENTS.map((x, i) => [i + 1, kindKey(x)]));
    this.lines = 0;
    this.rawBytes = 0;
    this.engineMs = 0;
    this.checkMs = 0;
    this.maxCheckMs = 0;
    this.nChecks = 0;
    this.counts = new Map();       // the recount: `${res} ${w} ${service} ${level}` -> lines
    this.tail = [];
    this.news = [];
    this.newTemplates = [];
    this.episodes = [];
    this.ep = null;
    this.timeline = [];            // [t, state, pick, similarity] for every check where something stood out
    this.timelineOut = 0;
    this.pending = [];             // labels waiting for two minutes of their incident
    this.labelled = false;         // the scripted label is done or was not needed
    this.current = { t: 0, state: 'quiet', fp: [], matches: [], pick: null, deciding: null, episode: null };
    this.fileChanged = true;
    this.done = false;
    this.finished = null;
    this.check(0);
  }

  get t() { return this.shop.t; }

  addTruth(x, by) {
    const tr = { n: this.truth.length + 1, kind: x.kind, key: kindKey(x), provider: x.provider || null, host: x.host || null,
      at: x.at - TODAY_START, minutes: x.minutes, by, name: KINDS[x.kind].name + (x.kind === 'payments' ? ` (${x.provider})` : ''),
      knownAtStart: null, detected: null, named: null, namedAs: null, newAt: null, wrong: 0, wrongAs: null, episodes: [] };
    this.truth.push(tr);
    return tr;
  }

  // start begins an incident of a kind now, as a visitor would. Returns it.
  start(kind, opts = {}) {
    if (!KINDS[kind] || this.done) return null;
    const x = this.shop.add(kind, opts);
    const tr = this.addTruth(x, 'you');
    tr.knownAtStart = this.known(tr.key);
    this.news.push({ t: tr.at, kind: 'red', text: `You started an incident. ${begins(x)}` });
    return tr;
  }

  known(key) {
    for (const k of this.kindOf.values()) if (k === key) return true;
    return false;
  }

  // Simulated seconds until maxSeconds are done or budgetMs has passed.
  step(maxSeconds, budgetMs = 60) {
    if (this.done) return 0;
    const t0 = now();
    let secs = 0;
    while (secs < maxSeconds && now() - t0 < budgetMs) {
      const t = this.shop.t;
      for (const tr of this.truth) {
        if (tr.by === 'script' && tr.at === t) {
          tr.knownAtStart = this.known(tr.key);
          this.news.push({ t, kind: 'red', text: begins(tr) });
        }
        if (tr.at + tr.minutes * 60 === t) this.news.push({ t, kind: '', text: ends(tr) });
      }
      const ts = this.shop.ts;
      const lines = this.shop.second();
      if (!lines) { this.done = true; break; }
      this.recount(ts, lines);
      if (lines.length) {
        const e0 = now();
        const res = this.eng.putLines(lines.join('\n'), 'shop', this.seq + 1);
        this.engineMs += now() - e0;
        if (res.error) throw new Error(res.error);
        this.seq += lines.length;
        for (const nt of res.newTemplates || []) {
          this.newTemplates.push(nt);
          this.news.push({ t, kind: 'amber', text: `A kind of line never seen before: ${nt.level} in ${nt.service}, “${nt.text}”.` });
        }
      }
      secs++;
      const next = t + 1;
      if (next % CHECK_EVERY === 0 || next >= TODAY_DURATION) {
        const e0 = now();
        const c = this.eng.checkpoint();
        this.engineMs += now() - e0;
        if (c.error) throw new Error(c.error);
        this.labelsDue(next);
        this.check(next);
        if (this.opts.script && !this.labelled && next >= TODAY_LABEL.at) this.scriptedLabel(next);
      }
      if (next >= TODAY_DURATION) { this.done = true; this.closeEpisode(next); break; }
    }
    return secs;
  }

  recount(ts, lines) {
    const m = ts - (ts % 60), h = ts - (ts % 3600);
    for (const l of lines) {
      this.lines++;
      this.rawBytes += l.length + 1;
      const x = LINE_RE.exec(l);
      if (!x) continue;
      for (const k of [`60 ${m} ${x[2]} ${x[1]}`, `3600 ${h} ${x[2]} ${x[1]}`]) this.counts.set(k, (this.counts.get(k) || 0) + 1);
      if (x[1] === 'WARN' || x[1] === 'ERROR') {
        this.tail.push(l);
        if (this.tail.length > 4 * TAIL) this.tail = this.tail.slice(-TAIL);
      }
    }
  }

  rows(sql, args) {
    let st = this.stmts.get(sql);
    if (!st) { st = this.db.prepare(sql); this.stmts.set(sql, st); }
    if (args) st.bind(args);
    const out = [];
    while (st.step()) out.push(st.get([]));
    st.reset();
    return out;
  }

  // check asks the file what stands out and which incident it looks like, and keeps the answer.
  check(t) {
    const c0 = now();
    const fp = this.rows(FP_SQL).map(([service, level, template, nowRate, usual, score, text]) =>
      ({ service, level, template, text, now: nowRate, usual, score }));
    let matches = [];
    let deciding = null;
    if (fp.length) {
      matches = this.rows(DV_SQL).map(([id, title, fix, started, source, similarity, shared, keys]) =>
        ({ id, title, fix, started, source, similarity, shared, keys }));
      const best = matches[0];
      if (best && best.similarity >= PARTLY) {
        // Only incidents on the same side of MATCH and PARTLY as the best, so a tie-break never
        // turns a match into a partial one, or a partial one into new.
        const floor = best.similarity >= MATCH ? MATCH : PARTLY;
        const tied = matches.filter((m) => m.similarity >= Math.max(best.similarity - TIE, floor));
        if (tied.length > 1) deciding = this.tieBreak(tied, fp, matches);
      }
    }
    const ms = now() - c0;
    this.checkMs += ms;
    if (ms > this.maxCheckMs) this.maxCheckMs = ms;
    this.nChecks++;
    const pick = matches[0] || null;
    const state = !fp.length ? 'quiet' : pick && pick.similarity >= MATCH ? 'match' : pick && pick.similarity >= PARTLY ? 'partly' : 'new';
    this.current = { t, state, fp, matches: matches.slice(0, 3), pick: pick && state !== 'new' ? pick.id : null, deciding, episode: null, ms };
    if (state === 'quiet') {
      if (this.ep && this.ep.end == null && t - this.ep.last >= QUIET_AFTER) this.closeEpisode(t);
      return;
    }
    if (!this.ep || this.ep.end != null) {
      this.ep = { id: this.episodes.length + 1, start: t, last: t, end: null, label: null, truth: [], states: { new: 0, partly: 0, match: 0 },
        firstState: state, shown: new Set(),
        what: fp.slice(0, 4).map((f) => ({ service: f.service, level: f.level, text: f.text, now: f.now, usual: f.usual })) };
      this.episodes.push(this.ep);
    }
    const ep = this.ep;
    ep.last = t;
    ep.states[state]++;
    this.current.episode = ep.id;
    this.timeline.push([t, state, this.current.pick, pick ? Number(pick.similarity.toFixed(3)) : 0]);
    // Which of today's incidents this is part of, as far as anyone could tell afterwards.
    for (const tr of this.truth) {
      if (t < tr.at || t > tr.at + tr.minutes * 60 + GRACE) continue;
      if (!ep.truth.includes(tr.n)) { ep.truth.push(tr.n); tr.episodes.push(ep.id); }
      if (tr.detected == null) tr.detected = t - tr.at;
      if (state === 'new' && tr.newAt == null) tr.newAt = t - tr.at;
      if (state === 'match') {
        if (this.kindOf.get(pick.id) === tr.key) {
          if (tr.named == null) { tr.named = t - tr.at; tr.namedAs = pick.id; }
        } else { tr.wrong++; tr.wrongAs ??= pick.id; }
      }
    }
    this.say(ep, state, pick, fp, deciding, t);
  }

  // say adds to the news when an episode starts to look like something, or like something else.
  say(ep, state, pick, fp, deciding, t) {
    const tr = this.truth.find((x) => ep.truth.includes(x.n));
    const after = tr ? `, ${t - tr.at} s after it began` : '';
    // Once an episode is named, the fading minutes after it are not news.
    const shown = state === 'new' ? 'new' : `${state}:${pick.id}`;
    if (ep.shown.has(shown) || (state !== 'match' && ep.states.match > 0)) return;
    ep.shown.add(shown);
    const sim = pick ? pick.similarity.toFixed(2) : '';
    if (state === 'match') {
      const why = deciding && deciding.words.length ? ` The raw lines tell it from #${deciding.other}: ${deciding.words.join(', ')}.` : '';
      this.news.push({ t, kind: 'teal', text: `Looks like #${pick.id}, “${pick.title}” (${when(pick.started)}), similarity ${sim}${after}. What fixed it: ${pick.fix}${why}` });
    } else if (state === 'partly') {
      this.news.push({ t, kind: 'amber', text: `Something stands out. Partly like #${pick.id}, “${pick.title}”, similarity ${sim}${after}.` });
    } else {
      const what = fp.slice(0, 2).map((f) => `“${f.text}”`).join(' and ');
      this.news.push({ t, kind: 'violet', text: `Something stands out that is like nothing in the file${after}: ${what}. Written down, it will be recognized next time.` });
    }
  }

  // tieBreak orders incidents that are about equally similar by the words their templates hide:
  // the newest raw line of each template now, against the example saved when each was written down.
  tieBreak(tied, fp, matches) {
    const standing = new Set(fp.map((f) => `${f.service} ${f.level} ${f.template}`));
    for (const m of tied) {
      m.same = 0;
      m.total = 0;
      m.pairs = [];
      for (const [service, level, template, text, example, latest] of this.rows(WORDS_SQL, [m.id])) {
        if (!standing.has(`${service} ${level} ${template}`)) continue;
        const a = hiddenWords(text, latest), b = hiddenWords(text, example);
        if (!a.length || a.length !== b.length) continue;
        m.pairs.push({ template, now: a, then: b });
        for (let i = 0; i < a.length; i++) { m.total++; if (a[i] === b[i]) m.same++; }
      }
    }
    const share = (m) => (m.total ? m.same / m.total : 0);
    tied.sort((a, b) => share(b) - share(a) || b.similarity - a.similarity || b.started - a.started);
    const order = [...tied, ...matches.filter((m) => !tied.includes(m))];
    matches.splice(0, matches.length, ...order);
    const [first, second] = tied;
    if (share(first) === share(second)) return null;
    // The words the chosen incident shares with now and the next one does not.
    const words = [];
    for (const p of first.pairs) {
      const q = second.pairs.find((x) => x.template === p.template);
      p.now.forEach((w, i) => { if (w === p.then[i] && (!q || q.then[i] !== w) && !words.includes(w)) words.push(w); });
    }
    return { id: first.id, other: second.id, words };
  }

  closeEpisode(t) {
    if (!this.ep || this.ep.end != null) return;
    this.ep.end = Math.min(t, this.ep.last);
  }

  // label writes down an episode as an incident, as the on-call team or a visitor would. Its
  // fingerprint is taken once two minutes of it are in the file.
  label(episode, title, fix, source = 'you') {
    const ep = this.episodes.find((e) => e.id === episode);
    title = String(title || '').trim().slice(0, 120);
    fix = String(fix || '').trim().slice(0, 300);
    if (!ep) return { error: 'There is no such episode.' };
    if (!title || !fix) return { error: 'Write what it was and what fixed it.' };
    if (ep.label || this.pending.some((p) => p.ep === ep)) return { error: 'It is written down already.' };
    const started = TODAY_START + ep.start;
    const due = Math.floor(started / 60) * 60 + FINGERPRINT + CHECK_EVERY - TODAY_START;
    const p = { ep, title, fix, source, due };
    if (this.t >= due) return this.insertLabel(p, this.t);
    this.pending.push(p);
    return { pending: true, due };
  }

  labelsDue(t) {
    for (const p of this.pending.filter((x) => t >= x.due)) {
      this.pending.splice(this.pending.indexOf(p), 1);
      this.insertLabel(p, t);
    }
  }

  insertLabel(p, t) {
    const { ep, title, fix, source } = p;
    const started = TODAY_START + ep.start;
    const ended = ep.end != null ? TODAY_START + ep.end : null;
    this.db.exec({ sql: 'INSERT INTO incidents (started, ended, title, fix, source) VALUES (?, ?, ?, ?, ?)', bind: [started, ended, title, fix, source] });
    const id = this.db.selectValue('SELECT max(id) FROM incidents');
    const keys = this.db.selectValue('SELECT count(*) FROM incident_keys WHERE incident = ?', [id]);
    const examples = this.db.selectValue('SELECT count(example) FROM incident_keys WHERE incident = ?', [id]);
    const tr = this.truth.find((x) => ep.truth.includes(x.n));
    this.kindOf.set(id, tr ? tr.key : null);
    ep.label = { id, title, source, at: t };
    this.fileChanged = true;
    const who = source === 'you' ? 'You wrote' : 'The on-call team writes';
    this.news.push({ t, kind: 'teal', text: keys
      ? `${who} it down as #${id}, “${title}”. Its fingerprint of ${keys} template${keys === 1 ? '' : 's'} is saved in the file${examples ? `, with an example line of each` : ''}.`
      : `${who} it down as #${id}, “${title}”. Nothing stood out in its first two minutes, so it has no fingerprint to match.` });
    return { id, keys, examples };
  }

  // The on-call team writes down today's new kind of incident once it is over, unless someone did.
  scriptedLabel(t) {
    this.labelled = true;
    const ep = [...this.episodes].reverse().find((e) => e.truth.some((n) => this.truth[n - 1].kind === TODAY_LABEL.kind));
    if (!ep) return;
    if (ep.label || this.pending.some((p) => p.ep === ep)) {
      this.news.push({ t, kind: '', text: 'The on-call team sees you wrote the certificate incident down already.' });
      return;
    }
    this.insertLabel({ ep, title: TODAY_LABEL.title, fix: TODAY_LABEL.fix, source: 'on-call team' }, t);
  }

  bytes() {
    return (this.db.selectValue('PRAGMA page_count') - this.db.selectValue('PRAGMA freelist_count')) * this.pageSize;
  }

  incidents() {
    return this.db.selectObjects(`SELECT i.id, i.started, i.ended, i.title, i.fix, i.source, count(k.template) AS keys, count(k.example) AS examples
      FROM incidents i LEFT JOIN incident_keys k ON k.incident = i.id GROUP BY i.id ORDER BY i.id`);
  }

  episodeView(ep) {
    return { id: ep.id, start: ep.start, last: ep.last, end: ep.end, label: ep.label, truth: ep.truth, states: ep.states, what: ep.what,
      pending: this.pending.some((p) => p.ep === ep) };
  }

  snapshot() {
    const news = this.news;
    this.news = [];
    const timeline = this.timeline.slice(this.timelineOut);
    this.timelineOut = this.timeline.length;
    const s = {
      t: this.shop.t,
      duration: TODAY_DURATION,
      lines: this.lines,
      rawBytes: this.rawBytes,
      engineMs: this.engineMs,
      checks: this.nChecks,
      checkMs: this.checkMs,
      maxCheckMs: this.maxCheckMs,
      fileBytes: this.bytes(),
      templates: this.db.selectValue('SELECT count(*) FROM _precomputing_templates'),
      current: this.current,
      episodes: this.episodes.map((e) => this.episodeView(e)),
      truth: this.truth.filter((x) => x.at <= this.shop.t),
      tail: this.tail.slice(-TAIL).reverse(),
      timeline,
      news,
      done: this.done,
    };
    if (this.fileChanged) { s.incidents = this.incidents(); this.fileChanged = false; }
    return s;
  }

  // The recount against the file: every count by service and level, minute by minute and hour by hour.
  checkCounts() {
    const got = new Map();
    for (const [res, w, service, level, n] of this.db.selectArrays(
      'SELECT res, w, service, level, sum(n) FROM lines_win WHERE res IN (60, 3600) AND w >= ? GROUP BY res, w, service, level', [TODAY_START])) {
      got.set(`${res} ${w} ${service} ${level}`, n);
    }
    const out = { cells: 0, same: 0, first: null, minute: { cells: 0, same: 0, windows: new Set() }, hour: { cells: 0, same: 0, windows: new Set() } };
    for (const k of new Set([...this.counts.keys(), ...got.keys()])) {
      const [res, w] = k.split(' ');
      const part = res === '60' ? out.minute : out.hour;
      part.windows.add(w);
      out.cells++; part.cells++;
      if (this.counts.get(k) === got.get(k)) { out.same++; part.same++; }
      else if (!out.first) out.first = `${k}: recount ${this.counts.get(k)}, file ${got.get(k)}`;
    }
    for (const part of [out.minute, out.hour]) part.windows = part.windows.size;
    out.minutes = out.minute.windows;
    return out;
  }

  finish() {
    if (this.finished) return this.finished;
    while (!this.done) this.step(1e9, 1e9);
    const tables = this.db.selectArrays('SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY name')
      .filter(([n]) => !n.startsWith('sqlite_'))
      .map(([name, bytes]) => ({ name, bytes, rows: this.db.selectValue("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", [name]) ? this.db.selectValue(`SELECT count(*) FROM "${name}"`) : null }));
    const outcomes = this.truth.map((tr) => ({ ...tr, ok: tr.knownAtStart ? tr.named != null && tr.wrong === 0 : tr.newAt != null && tr.wrong === 0 }))
      .sort((a, b) => a.at - b.at || a.n - b.n);
    const falseAlarms = this.episodes.filter((e) => !e.truth.length).map((e) => this.episodeView(e));
    this.finished = {
      lines: this.lines,
      historyLines: this.historyLines,
      rawBytes: this.rawBytes,
      engineMs: this.engineMs,
      checks: this.nChecks,
      checkMs: this.checkMs,
      maxCheckMs: this.maxCheckMs,
      fileBytes: this.bytes(),
      templates: this.db.selectValue('SELECT count(*) FROM _precomputing_templates'),
      outcomes,
      falseAlarms,
      episodes: this.episodes.length,
      counts: this.checkCounts(),
      timeline: this.timeline,
      incidents: this.incidents(),
      tables,
      sqlite: this.sqlite3.version.libVersion,
    };
    return this.finished;
  }

  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    const refused = { error: 'This box only reads, one statement at a time. The file changes through its streams and when an incident is written down.' };
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

  policy() { return this.db.selectValue("SELECT value FROM _precomputing WHERE key = 'policy'"); }

  exportFile() { return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer); }
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
// when writes a time in seconds as 29 Sep, 14:10 (UTC).
export function when(ts) {
  const d = new Date(ts * 1000);
  return `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}, ${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
}
