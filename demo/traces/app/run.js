// One run of Demo 8: a day of coding-agent runs through the store of agent calls and the Engine
// (Go, built for the browser), which keep their file in SQLite's WebAssembly build. The store keeps
// every message and tool list once, under the SHA-256 of its bytes, masks secrets before anything
// is stored, and meters every call through the policy's exact streams. Runs in the page's worker
// and, unchanged, in Node, which is how the published numbers are made (tools/run-demo8.mjs).
import { startGo, sqliteStore } from '../../lib/engine.js';

export const DAY = Date.UTC(2026, 8, 29) / 1000;   // 29 September 2026, 00:00 UTC
export const PERIOD = '2026-09-29';
export const OPEN = DAY + 7 * 3600;                // the page starts the day at 07:00
export const CHECKPOINT = 60;                      // seconds of the day between checkpoints
export const BUDGET = 200e6;                       // each repository's budget for the day: $0.20, in billionths of a dollar
export const RAISED = 1e9;                         // a raised budget: $1.00
export const STUCK = 40;                           // a run that reaches this many calls is news
// The policy's example prices, in billionths of a dollar per token (examples/traces.precompute).
export const PRICE = { input: 400, cached: 40, output: 1600 };
export const CACHE_WINDOW = 300;                   // seconds a provider keeps a call's input cached
const TRACE_TABLES = ['trace_pieces', 'sqlite_autoindex_trace_pieces_1', 'trace_calls', 'trace_calls_run'];
const now = () => performance.now();

export const costOf = (u) => (u.input - u.cached) * PRICE.input + u.cached * PRICE.cached + u.output * PRICE.output;

// parseDay reads the runs, one JSON line each, and lists every model call: the calls are the
// assistant messages after the first message, in the order their replies came back.
export function parseDay(text) {
  const runs = [];
  for (const line of text.split('\n')) {
    if (!line) continue;
    const r = JSON.parse(line);
    r.replies = [];
    r.messages.forEach((m, i) => { if (i > 0 && m.role === 'assistant') r.replies.push(i); });
    runs.push(r);
  }
  const calls = [];
  for (const r of runs) r.replies.forEach((i, k) => calls.push({ run: r, seq: k + 1, i, ts: r.start + r.messages[i].at, id: `${r.id}#${k + 1}` }));
  calls.sort((a, b) => a.ts - b.ts || (a.run.id < b.run.id ? -1 : a.run.id > b.run.id ? 1 : a.seq - b.seq));
  return { runs, calls };
}

// usageOf works out what the call answered by message i costs in tokens, the way a provider
// reports it: the input is the tool list and every message before i; the provider serves from its
// cache the whole input of the run's previous call when that call was sent at most five minutes
// before. Written here on its own, from the rule, to recount what the file says.
export function usageOf(run, i) {
  const m = run.messages;
  const sent = run.start + m[i - 1].at;
  let prev = -1;
  for (let j = i - 1; j > 0; j--) if (m[j].role === 'assistant') { prev = j; break; }
  const cachedUpTo = prev > 0 && sent - (run.start + m[prev - 1].at) <= CACHE_WINDOW ? prev : -1;
  const sources = new Map();
  let input = 0, cached = 0;
  const add = (src, tokens, isCached) => {
    const s = sources.get(src) || { tokens: 0, cached: 0 };
    s.tokens += tokens;
    input += tokens;
    if (isCached) { s.cached += tokens; cached += tokens; }
    sources.set(src, s);
  };
  add('tools', run.tools_tokens, cachedUpTo >= 0);
  for (let j = 0; j < i; j++) add(m[j].source, m[j].tokens, j < cachedUpTo);
  return { input, cached, output: m[i].tokens, sources, ts: run.start + m[i].at, sent };
}

// masker runs the store's secret patterns (from the Go build) with JavaScript's own regular
// expressions: what each piece should look like in the file.
export function masker(patterns) {
  const res = patterns.map((p) => ({ name: p.name, re: new RegExp(p.re, 'g'), with: p.keep ? '$1' + p.label : p.label }));
  const mask = (s) => {
    let n = 0;
    const found = [];
    for (const p of res) {
      const m = s.match(p.re);
      if (!m) continue;
      n += m.length;
      for (let k = 0; k < m.length; k++) found.push(p.name);
      s = s.replace(p.re, p.with);
    }
    return { text: s, n, found };
  };
  // left finds what still looks like a secret: a match that is not a label already.
  const left = (s) => {
    const out = [];
    for (const p of res) for (const m of s.matchAll(p.re)) if (!m[0].includes('[redacted')) out.push(p.name);
    return out;
  };
  return { mask, left };
}

// requestOf writes the call answered by message i as the agent sent it, masked: the model, the
// messages before the reply and the tool list, each piece as it came.
export function requestOf(run, i) {
  return `{"model":${JSON.stringify(run.model)},"messages":[${run.masked.slice(0, i).join(',')}],"tools":${run.maskedTools}}`;
}

const SECRET_WORDS = { 'aws-key-id': 'an AWS key id', 'aws-secret': 'an AWS secret key', 'github-token': 'a GitHub token', 'api-key': 'an API key',
  'private-key': 'a private key', 'slack-token': 'a Slack token', 'bearer-token': 'a bearer token' };
const listOf = (xs) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`);
const usd = (nano) => `$${(nano / 1e9).toFixed(2)}`;
const hhmm = (ts) => new Date(ts * 1000).toISOString().slice(11, 16);

export class Run {
  // module: the compiled Go program (loadGo); files: {policy, day} texts.
  constructor(sqlite3, module, files) {
    this.sqlite3 = sqlite3;
    this.module = module;
    this.files = files;
    this.db = null;
  }

  async init() {
    this.go = await startGo(this.module);
    this.compiled = this.go.compile(this.files.policy, 'traces.precompute');
    if (this.compiled.error) throw new Error(this.compiled.error);
    const { runs, calls } = parseDay(this.files.day);
    this.runs = runs;
    this.calls = calls;
    this.byId = new Map(runs.map((r) => [r.id, r]));
    this.repos = [...new Set(runs.map((r) => r.repo))].sort();
    this.reset();
    // Mask every message the way the store should: the pieces the file must hold.
    this.masks = masker(this.eng.patterns());
    this.planted = 0;
    for (const r of runs) {
      r.secrets = [];
      r.masked = r.messages.map((m, j) => {
        const x = this.masks.mask(m.raw);
        if (x.n) {
          this.planted += x.n;
          // The message goes into the file with the first call that sends it: the next reply's.
          r.secrets.push({ at: r.replies.findIndex((i) => i > j) + 1, found: x.found, source: m.source });
        }
        return x.text;
      });
      const tx = this.masks.mask(r.tools);
      this.planted += tx.n;
      r.maskedTools = tx.text;
    }
    return this;
  }

  reset() {
    this.close();
    this.db = new this.sqlite3.oo1.DB(':memory:');
    this.store = sqliteStore(this.sqlite3, this.db);
    this.eng = this.go.traces(this.store, this.files.policy, 'traces.precompute');
    if (this.eng.error) throw new Error(this.eng.error);
    const d = this.eng.load(this.files.day);
    if (d.error) throw new Error(d.error);
    if (d.calls !== this.calls.length) throw new Error(`the Engine sees ${d.calls} calls, the page ${this.calls.length}`);
    this.end = (Math.floor((d.last - DAY) / CHECKPOINT) + 1) * CHECKPOINT + DAY;
    // Budgets: the same for every repository, in the limit table of the policy's quota.
    this.db.exec('BEGIN');
    for (const r of this.repos) this.db.exec({ sql: 'INSERT INTO repo_budget_limit (repo, lim) VALUES (?, ?)', bind: [r, BUDGET] });
    this.db.exec('COMMIT');
    this.stmts = new Map();
    this.pageSize = this.db.selectValue('PRAGMA page_size');
    this.t = OPEN;
    this.next = 0;
    this.progress = new Map();   // run id -> calls handed over
    this.engineMs = 0;
    this.checkpoints = 0;
    this.news = [];
    this.alerts = [];
    this.alerted = new Set();
    this.raised = new Set();
    this.again = [];
    this.stuck = new Set();
    this.done = false;
    this.finished = null;
  }

  close() {
    if (this.eng && !this.eng.error) this.eng.close();
    for (const st of this.stmts?.values() || []) st.finalize();
    this.stmts = new Map();
    if (this.db) this.db.close();
    this.eng = this.store = this.db = null;
  }

  rows(sql, args) {
    let st = this.stmts.get(sql);
    if (!st) { st = this.db.prepare(sql); this.stmts.set(sql, st); }
    if (args && args.length) st.bind(args);
    const out = [];
    try {
      while (st.step()) {
        const r = st.get([]);
        for (let i = 0; i < r.length; i++) if (typeof r[i] === 'bigint') r[i] = Number(r[i]);
        out.push(r);
      }
    } finally {
      st.reset();
    }
    return out;
  }

  value(sql, args) { const r = this.rows(sql, args); return r.length ? r[0][0] : null; }

  // Seconds of the day until maxSeconds are done or budgetMs has passed.
  step(maxSeconds, budgetMs = 60) {
    if (this.done) return 0;
    const t0 = now();
    let secs = 0;
    while (secs < maxSeconds && now() - t0 < budgetMs) {
      const boundary = this.t - ((this.t - DAY) % CHECKPOINT) + CHECKPOINT;
      const to = Math.min(boundary, this.t + Math.max(1, Math.floor(maxSeconds - secs)), this.end);
      const e0 = now();
      const r = this.eng.until(to);
      this.engineMs += now() - e0;
      if (r.error) throw new Error(r.error);
      for (let k = this.next; k < r.next; k++) this.handed(this.calls[k]);
      this.next = r.next;
      secs += to - this.t;
      this.t = to;
      if ((to - DAY) % CHECKPOINT === 0 || to >= this.end) this.checkpoint();
      if (to >= this.end) { this.done = true; break; }
    }
    return secs;
  }

  // handed notes a call the store has just kept: a secret it masked, a run that will not stop.
  handed(c) {
    const r = c.run;
    this.progress.set(r.id, c.seq);
    for (const s of r.secrets) {
      if (s.at !== c.seq) continue;
      const what = listOf([...new Set(s.found)].map((x) => SECRET_WORDS[x] || x));
      const where = s.source.startsWith('tool:') ? `A tool's output in run ${r.id} (${r.repo})` : `A message in run ${r.id} (${r.repo})`;
      this.news.push({ t: c.ts, kind: 'amber', text: `${where} held ${what}. ${s.found.length > 1 ? 'Both were' : 'It was'} masked before call ${c.seq} reached the file.` });
    }
    if (c.seq === STUCK && !this.stuck.has(r.id)) {
      this.stuck.add(r.id);
      const u = usageOf(r, c.i);
      this.news.push({ t: c.ts, kind: 'red', text: `Run ${r.id} on ${r.repo} has made ${STUCK} calls in ${Math.round((c.ts - r.start) / 60)} minutes, and each call now sends ${Math.round(u.input / 1000)}k tokens: the agent runs the whole test suite again and again.` });
    }
  }

  checkpoint() {
    const e0 = now();
    const r = this.eng.checkpoint();
    this.engineMs += now() - e0;
    this.checkpoints++;
    if (r.error) throw new Error(r.error);
    // One read of the quota view, as a gateway would make before letting a run's next call through.
    for (const [repo, used, lim] of this.rows('SELECT repo, used, lim FROM repo_budget WHERE reached AND period = ?', [PERIOD])) {
      if (this.alerted.has(repo)) continue;
      this.alerted.add(repo);
      const [callId, run, ts] = this.crossing(repo, lim);
      const a = { repo, lim, used, callId, run, ts, seen: this.t };
      this.alerts.push(a);
      this.news.push({ t: ts, kind: 'red', text: `Alert: ${repo} has spent its budget of ${usd(lim)} for the day. Call ${callId.split('#')[1]} of run ${run}, at ${hhmm(ts)}, took it past. A gateway that reads repo_budget before each call would stop the run here.` });
    }
  }

  // crossing finds the call that took a repository past a limit, from the calls kept whole.
  crossing(repo, lim) {
    const r = this.rows(`SELECT call_id, run, ts FROM (SELECT call_id, run, ts,
        sum((input_tokens - cached_tokens) * ${PRICE.input} + cached_tokens * ${PRICE.cached} + output_tokens * ${PRICE.output}) OVER (ORDER BY ts, call_id ROWS UNBOUNDED PRECEDING) AS spent
      FROM calls WHERE repo = ?) WHERE spent >= ? ORDER BY ts, call_id LIMIT 1`, [repo, lim]);
    return r[0] || ['', '', this.t];
  }

  // finishedRuns lists the runs whose every call is in the file, the latest first.
  finishedRuns() {
    return this.runs.filter((r) => this.progress.get(r.id) === r.replies.length)
      .sort((a, b) => b.start + b.messages[b.messages.length - 1].at - (a.start + a.messages[a.messages.length - 1].at));
  }

  // reportAgain sends the calls of the latest finished run to the store a second time, as a
  // tracer that retries an upload would.
  reportAgain() {
    const done = this.finishedRuns();
    const r = done.find((x) => !this.again.some((a) => a.run === x.id)) || done[0];
    if (!r) return null;
    const before = this.value('SELECT value FROM run_cost WHERE run = ?', [r.id]);
    const refusedBefore = this.value("SELECT coalesce(sum(n), 0) FROM calls_refused WHERE reason = 'repeat'");
    const res = this.eng.again(r.id);
    if (res.error) throw new Error(res.error);
    this.checkpoint();
    const after = this.value('SELECT value FROM run_cost WHERE run = ?', [r.id]);
    const refused = this.value("SELECT coalesce(sum(n), 0) FROM calls_refused WHERE reason = 'repeat'") - refusedBefore;
    const a = { run: r.id, repo: r.repo, calls: res.calls, refused, before, after, t: this.t };
    this.again.push(a);
    this.news.push({ t: this.t, kind: 'teal', text: `You reported run ${r.id} again: its ${res.calls} calls went to the store a second time. The store knew them and the meter refused ${refused} as repeats; the run still costs ${usd(after)}.` });
    return a;
  }

  // raise lifts a repository's budget for the day: one row of the quota's limit table.
  raise(repo) {
    this.db.exec({ sql: 'UPDATE repo_budget_limit SET lim = ? WHERE repo = ?', bind: [RAISED, repo] });
    this.raised.add(repo);
    const used = this.value('SELECT used FROM repo_budget WHERE repo = ? AND period = ?', [repo, PERIOD]) || 0;
    this.news.push({ t: this.t, kind: 'teal', text: `You raised the budget of ${repo} to ${usd(RAISED)} for the day. repo_budget now shows ${usd(used)} used; the gateway would let its calls through again.` });
  }

  traceBytes() {
    return this.value(`SELECT coalesce(sum(pgsize), 0) FROM dbstat WHERE name IN (${TRACE_TABLES.map(() => '?').join(', ')})`, TRACE_TABLES);
  }

  bytes() {
    return (this.db.selectValue('PRAGMA page_count') - this.db.selectValue('PRAGMA freelist_count')) * this.pageSize;
  }

  // What the page shows, read from the file.
  snapshot(withBytes = true) {
    const st = this.eng.traceStats();
    const repos = this.rows(`SELECT u.repo, u.value, l.lim, u.value >= l.lim, c.value FROM repo_cost_day u JOIN repo_budget_limit l USING (repo)
      JOIN repo_calls_day c ON c.repo = u.repo AND c.period = u.period WHERE u.period = ? ORDER BY u.value DESC, u.repo`, [PERIOD])
      .map(([repo, cost, lim, reached, calls]) => ({ repo, cost, lim, reached: !!reached, calls }));
    const sources = this.rows(`SELECT t.source, t.value, c.value FROM source_tokens_day t JOIN source_cost_day c ON c.source = t.source AND c.period = t.period
      WHERE t.period = ? ORDER BY c.value DESC, t.source`, [PERIOD]).map(([source, tokens, cost]) => ({ source, tokens, cost }));
    const runs = this.rows(`SELECT c.run, c.value, i.value, k.value, o.value, x.value FROM run_calls c JOIN run_input i USING (run) JOIN run_cached k USING (run)
      JOIN run_output o USING (run) JOIN run_cost x USING (run) ORDER BY x.value DESC, c.run LIMIT 8`)
      .map(([id, calls, input, cached, output, cost]) => {
        const r = this.byId.get(id);
        return { id, repo: r.repo, task: r.task, calls, total: r.replies.length, input, cached, output, cost, resolved: r.resolved, running: calls < r.replies.length };
      });
    const news = this.news;
    this.news = [];
    return {
      t: this.t,
      open: OPEN,
      end: this.end,
      calls: st.calls,
      callsTotal: this.calls.length,
      runsStarted: this.runs.filter((r) => r.start <= this.t).length,
      runsTotal: this.runs.length,
      repeats: st.repeats,
      masked: st.masked,
      requestBytes: st.requestBytes,
      pieceBytes: st.pieceBytes,
      pieces: st.pieces,
      traceBytes: withBytes ? this.traceBytes() : null,
      fileBytes: withBytes ? this.bytes() : null,
      cost: this.value('SELECT coalesce(sum(value), 0) FROM repo_cost_day'),
      engineMs: this.engineMs,
      repos,
      sources,
      runs,
      alerts: this.alerts,
      raised: [...this.raised],
      again: this.again,
      finished: this.finishedRuns().map((r) => ({ id: r.id, repo: r.repo, calls: r.replies.length })),
      news,
      done: this.done,
    };
  }

  // rebuild reads one call back from the file with the trace_requests view, and compares it with
  // the request as the agent sent it, masked here from the day's runs.
  async rebuild(callId) {
    const t0 = now();
    const row = this.rows(`SELECT r.request, r.reply, c.request_sha256, c.request_bytes, c.messages, c.tools, c.input_tokens, c.cached_tokens, c.output_tokens, c.ts
      FROM trace_requests r JOIN trace_calls c USING (call_id) WHERE r.call_id = ?`, [callId])[0];
    const ms = now() - t0;
    if (!row) return { callId, error: `The file has no call ${callId} yet.` };
    const [request, reply, stored, bytes, messages, tools, input, cached, output, ts] = row;
    const parts = this.rows(`SELECT p.source, count(*), sum(p.bytes) FROM (SELECT value AS id FROM json_each(?) UNION ALL SELECT ?) AS j
      JOIN trace_pieces p ON p.id = j.id GROUP BY p.source ORDER BY 3 DESC`, [messages, tools]).map(([source, n, b]) => ({ source, n, bytes: b }));
    const [runId, seq] = callId.split('#');
    const run = this.byId.get(runId);
    const sent = requestOf(run, run.replies[Number(seq) - 1]);
    const [shaFile, shaSent] = await Promise.all([sha256(request), sha256(sent)]);
    return {
      callId, run: runId, seq: Number(seq), calls: run.replies.length, repo: run.repo, ts, ms,
      request, reply, bytes, pieces: JSON.parse(messages).length + 1, parts, input, cached, output,
      same: request === sent, sentBytes: new TextEncoder().encode(sent).length, stored, shaFile, shaSent,
    };
  }

  // finish runs the rest of the day at full speed and checks the file against the day's runs.
  finish() {
    if (this.finished) return this.finished;
    while (!this.done) this.step(1e9, 1e9);
    const t0 = now();
    // 1. Every call, rebuilt from the file with SQL alone, against the request as sent.
    const byCall = new Map(this.calls.map((c) => [c.id, c]));
    let rebuilt = 0, same = 0, compared = 0;
    const differ = [];
    const all = this.db.prepare('SELECT call_id, request FROM trace_requests');
    try {
      while (all.step()) {
        const id = all.get(0), request = all.get(1);
        rebuilt++;
        const c = byCall.get(id);
        const want = c ? requestOf(c.run, c.i) : null;
        compared += request.length;
        if (request === want) same++; else if (differ.length < 3) differ.push(id);
      }
    } finally {
      all.finalize();
    }
    const rebuildMs = now() - t0;
    // 2. Secrets: every piece in the file searched with every pattern.
    const left = [];
    for (const [id, body] of this.rows('SELECT id, body FROM trace_pieces')) for (const name of this.masks.left(body)) left.push({ id, name });
    // 3. The recount: every call's usage from the rule, added up by run, repository, source and model.
    const add = (m, k, f) => { const x = m.get(k) || { calls: 0, input: 0, cached: 0, output: 0, cost: 0, tokens: 0 }; f(x); m.set(k, x); };
    const byRun = new Map(), byRepo = new Map(), bySource = new Map(), byModel = new Map();
    for (const c of this.calls) {
      const u = usageOf(c.run, c.i);
      const cost = costOf(u);
      const call = (x) => { x.calls++; x.input += u.input; x.cached += u.cached; x.output += u.output; x.cost += cost; };
      add(byRun, c.run.id, call);
      add(byRepo, c.run.repo, call);
      add(byModel, c.run.model, call);
      for (const [src, s] of u.sources) add(bySource, src, (x) => { x.tokens += s.tokens; x.cost += (s.tokens - s.cached) * PRICE.input + s.cached * PRICE.cached; });
      add(bySource, 'output', (x) => { x.cost += u.output * PRICE.output; });
    }
    const cmp = (name, sql, want, fields) => {
      const got = new Map(this.rows(sql).map((r) => [r[0], r.slice(1)]));
      let equal = 0;
      for (const [k, x] of want) if (got.has(k) && fields.every((f, j) => got.get(k)[j] === x[f])) equal++;
      return { name, n: want.size, equal, extra: [...got.keys()].filter((k) => !want.has(k)).length };
    };
    const checks = [
      cmp('Cost of each run', 'SELECT run, value FROM run_cost', byRun, ['cost']),
      cmp('Calls and tokens of each run', `SELECT c.run, c.value, i.value, k.value, o.value FROM run_calls c JOIN run_input i USING (run)
        JOIN run_cached k USING (run) JOIN run_output o USING (run)`, byRun, ['calls', 'input', 'cached', 'output']),
      cmp('Calls and cost of each repository, for the day', `SELECT u.repo, c.value, u.value FROM repo_cost_day u JOIN repo_calls_day c ON c.repo = u.repo AND c.period = u.period WHERE u.period = '${PERIOD}'`, byRepo, ['calls', 'cost']),
      cmp('Tokens and cost of each source', `SELECT t.source, t.value, c.value FROM source_tokens_day t JOIN source_cost_day c ON c.source = t.source AND c.period = t.period WHERE t.period = '${PERIOD}'`, bySource, ['tokens', 'cost']),
      cmp('Cost of each model', `SELECT model, value FROM model_cost_day WHERE period = '${PERIOD}'`, byModel, ['cost']),
    ];
    const total = [...byRun.values()].reduce((a, x) => a + x.cost, 0);
    const byCallsStream = this.value('SELECT sum(value) FROM repo_cost_day');
    const bySourceStream = this.value('SELECT sum(value) FROM source_cost_day');
    const refused = Object.fromEntries(this.rows("SELECT 'calls', coalesce(sum(n), 0) FROM calls_refused WHERE reason = 'repeat' UNION ALL SELECT 'context', coalesce(sum(n), 0) FROM context_refused WHERE reason = 'repeat'"));
    // What the budget alerts would have saved: the calls after each crossing.
    const alerts = this.alerts.map((a) => {
      const [n, inRun, cost] = this.rows(`SELECT count(*), coalesce(sum(run = ?), 0), coalesce(sum((input_tokens - cached_tokens) * ${PRICE.input} + cached_tokens * ${PRICE.cached} + output_tokens * ${PRICE.output}), 0)
        FROM calls WHERE repo = ? AND (ts > ? OR (ts = ? AND call_id > ?))`, [a.run, a.repo, a.ts, a.ts, a.callId])[0];
      return { ...a, after: n, afterRun: inRun, afterCost: cost };
    });
    const tables = this.rows('SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY 2 DESC')
      .filter(([n]) => !n.startsWith('sqlite_stat'))
      .map(([name, bytes]) => ({ name, bytes, rows: this.value("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", [name]) ? this.value(`SELECT count(*) FROM "${name}"`) : null }));
    const st = this.eng.traceStats();
    this.finished = {
      runs: this.runs.length,
      calls: this.calls.length,
      repos: this.repos.length,
      messages: this.runs.reduce((a, r) => a + r.messages.length, 0),
      rebuilt, same, differ, comparedBytes: compared, rebuildMs,
      planted: this.planted, masked: st.masked, left,
      checks,
      total, byCallsStream, bySourceStream,
      again: this.again, refused,
      alerts,
      requestBytes: st.requestBytes, replyBytes: st.replyBytes, pieceBytes: st.pieceBytes, pieces: st.pieces,
      traceBytes: this.traceBytes(), fileBytes: this.bytes(),
      engineMs: this.engineMs, checkpoints: this.checkpoints,
      tables,
      sqlite: this.sqlite3.version.libVersion,
    };
    return this.finished;
  }

  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    if (!/^(select|with|pragma|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) {
      return { error: 'This box only reads, one statement at a time. The file changes only through the store and its streams.' };
    }
    try {
      const columns = [];
      const rows = [];
      const t0 = now();
      this.db.exec({ sql: text, rowMode: 'array', resultRows: rows, columnNames: columns });
      for (const r of rows) for (let i = 0; i < r.length; i++) {
        if (typeof r[i] === 'bigint') r[i] = Number(r[i]);
        if (typeof r[i] === 'string' && r[i].length > 2000) r[i] = `${r[i].slice(0, 2000)}… (${r[i].length.toLocaleString('en-US')} characters)`;
      }
      return { columns, rows: rows.slice(0, 500), total: rows.length, ms: now() - t0 };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    }
  }

  // The tables the store adds to the file, as SQLite holds them.
  traceSchema() {
    return this.rows("SELECT sql FROM sqlite_master WHERE name LIKE 'trace_%' AND sql IS NOT NULL ORDER BY rowid").map((r) => r[0] + ';').join('\n\n');
  }

  exportFile() { return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer); }
}

export async function sha256(text) {
  const d = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, '0')).join('');
}
