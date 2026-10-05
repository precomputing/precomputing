// One run of Demo 1: the compiled policy on SQLite, fed by the scenario.
// Runs the same way in the page's worker and in Node, which is how the
// published numbers are produced.

import { Scenario, START, DURATION, ENDPOINTS } from './scenario.js';

const now = () => performance.now();

// The comparison: every request kept whole in a plain table, the way logs usually are.
const RAW_SCHEMA = 'CREATE TABLE requests (ts INTEGER NOT NULL, endpoint TEXT NOT NULL, ms REAL NOT NULL)';

export class Run {
  constructor(sqlite3, files) {
    this.sqlite3 = sqlite3;
    this.files = files; // { schema, distill }
    this.reset();
  }

  reset() {
    this.close();
    const { oo1 } = this.sqlite3;
    this.pc = new oo1.DB(':memory:', 'c');
    this.pc.exec(this.files.schema);
    this.raw = new oo1.DB(':memory:', 'c');
    this.raw.exec(RAW_SCHEMA);
    const sk = JSON.parse(this.pc.selectValue("SELECT detail FROM _precomputing_objects WHERE name = 'latency_ms_sk'"));
    this.gamma = sk.gamma;
    this.pageSize = this.pc.selectValue('PRAGMA page_size');
    this.insPc = this.pc.prepare('INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?)');
    this.insRaw = this.raw.prepare('INSERT INTO requests (ts, endpoint, ms) VALUES (?, ?, ?)');
    this.minuteP99 = this.pc.prepare(
      `WITH c AS (SELECT endpoint, b, sum(n) OVER (PARTITION BY endpoint ORDER BY b ROWS UNBOUNDED PRECEDING) AS cum,
                         sum(n) OVER (PARTITION BY endpoint) AS tot
                  FROM latency_ms_sk WHERE res = 60 AND w = ?)
       SELECT endpoint, 2 * pow(${this.gamma}, min(b)) / (${this.gamma} + 1) FROM c WHERE cum >= 0.99 * tot GROUP BY endpoint`);
    this.minuteAnomalies = this.pc.prepare(
      'SELECT endpoint, count(*) FROM latency_anomaly WHERE ts >= ? AND ts < ? GROUP BY endpoint');
    // Every answer the table shows, read in one query from the three precompute views.
    this.answer = this.pc.prepare(
      'SELECT r.endpoint, r.value, a.value, p.value FROM requests r JOIN avg_ms a USING (endpoint) JOIN p99_ms p USING (endpoint)');
    // One count and one average: two single-row lookups.
    this.simple = this.pc.prepare('SELECT r.value, a.value FROM requests r, avg_ms a WHERE r.endpoint = ?1 AND a.endpoint = ?1');
    this.scenario = new Scenario();
    this.events = 0;
    this.series = [{ t: 0, p99: ENDPOINTS.map(() => null), kept: ENDPOINTS.map(() => 0), raw: this.bytes(this.raw, false), pc: this.bytes(this.pc, true) }];
    this.sent = 0;          // series points already handed to the page
    this.ingestMs = 0;
    this.done = false;
    this.finished = null;
  }

  close() {
    for (const s of ['insPc', 'insRaw', 'minuteP99', 'minuteAnomalies', 'answer', 'simple']) {
      if (this[s]) { try { this[s].finalize(); } catch (e) { /* already closed */ } this[s] = null; }
    }
    if (this.pc) { this.pc.close(); this.pc = null; }
    if (this.raw) { this.raw.close(); this.raw = null; }
  }

  setTraffic(rate) { this.scenario.rate = rate; }
  injectSpike() { return this.scenario.injectSpike(); }
  startOutage(e) { return this.scenario.startOutage(e); }

  // Process simulated seconds until maxSeconds are done or budgetMs has passed.
  step(maxSeconds, budgetMs = 60) {
    if (this.done) return 0;
    const t0 = now();
    const { insPc, insRaw, scenario } = this;
    let secs = 0;
    this.pc.exec('BEGIN');
    this.raw.exec('BEGIN');
    try {
      while (secs < maxSeconds && now() - t0 < budgetMs) {
        const evs = scenario.second();
        if (!evs) { this.done = true; break; }
        for (let i = 0; i < evs.length; i++) {
          const ev = evs[i];
          insPc.bind(1, ev[0]).bind(2, ev[1]).bind(3, ev[2]).stepReset();
          insRaw.bind(1, ev[0]).bind(2, ev[1]).bind(3, ev[2]).stepReset();
        }
        this.events += evs.length;
        secs++;
        if (scenario.t % 60 === 0) this.minute(scenario.t - 60);
        if (scenario.t % 60 === 0) this.distill(scenario.t);
        if (scenario.t >= DURATION) { this.done = true; break; }
      }
    } finally {
      this.pc.exec('COMMIT');
      this.raw.exec('COMMIT');
    }
    this.ingestMs += now() - t0;
    return secs;
  }

  // Detail that has outlived the policy fades; t is simulated seconds since START.
  distill(t) {
    this.pc.exec(this.files.distill.replaceAll(':now', String(START + t - 1)));
  }

  bytes(db, free) {
    const pages = db.selectValue('PRAGMA page_count') - (free ? db.selectValue('PRAGMA freelist_count') : 0);
    return pages * this.pageSize;
  }

  // Called as each simulated minute closes: its p99 per endpoint and the two file sizes.
  minute(t) {
    const w = START + t;
    const p99 = ENDPOINTS.map(() => null);
    this.minuteP99.bind(1, w);
    while (this.minuteP99.step()) p99[ENDPOINTS.indexOf(this.minuteP99.get(0))] = this.minuteP99.get(1);
    this.minuteP99.reset();
    const kept = ENDPOINTS.map(() => 0);
    this.minuteAnomalies.bind(1, w).bind(2, w + 60);
    while (this.minuteAnomalies.step()) kept[ENDPOINTS.indexOf(this.minuteAnomalies.get(0))] = this.minuteAnomalies.get(1);
    this.minuteAnomalies.reset();
    this.series.push({ t: t + 60, p99, kept, raw: this.bytes(this.raw, false), pc: this.bytes(this.pc, true) });
  }

  // The live answers, read straight from the precompute views, with the time the read takes.
  answers(reps = 1) {
    const st = this.answer;
    let rows = [];
    const t0 = now();
    for (let k = 0; k < reps; k++) {
      rows = [];
      while (st.step()) rows.push([st.get(0), st.get(1), st.get(2), st.get(3)]);
      st.reset();
    }
    const readMs = (now() - t0) / reps;
    const by = new Map(rows.map((r) => [r[0], r]));
    return {
      readMs,
      rows: ENDPOINTS.map((endpoint) => {
        const r = by.get(endpoint);
        return { endpoint, requests: r ? r[1] : 0, avg: r ? r[2] : null, p99: r ? r[3] : null };
      }),
    };
  }

  // Time for one count and one average of one endpoint: two single-row lookups.
  simpleReadUs(reps = 5000) {
    const st = this.simple;
    const t0 = now();
    for (let k = 0; k < reps; k++) {
      st.bind(1, ENDPOINTS[k % ENDPOINTS.length]);
      st.step();
      st.reset();
    }
    return ((now() - t0) / reps) * 1000;
  }

  snapshot() {
    const fresh = this.series.slice(this.sent);
    this.sent = this.series.length;
    return {
      t: this.scenario.t,
      duration: DURATION,
      events: this.events,
      rawBytes: this.bytes(this.raw, false),
      pcBytes: this.bytes(this.pc, true),
      anomalies: this.pc.selectValue('SELECT count(*) FROM latency_anomaly'),
      answers: this.answers(),
      series: fresh,
      rate: this.scenario.rate,
      ingestMs: this.ingestMs,
      outages: this.scenario.outages.map((o) => ({ endpoint: o.endpoint, from: o.from, to: o.to, visitor: !!o.visitor })),
      done: this.done,
    };
  }

  // Everything the results section shows, measured after the last event.
  finish() {
    if (this.finished) return this.finished;
    while (!this.done) this.step(1e9, 1e9);
    this.distill(DURATION);
    const beforeVacuum = this.bytes(this.pc, false);
    this.pc.exec('VACUUM');
    const pcBytes = this.bytes(this.pc, false);
    const rawBytes = this.bytes(this.raw, false);
    // The chart's last point shows the file as it ends up, compacted.
    const lastPoint = this.series[this.series.length - 1];
    lastPoint.pc = pcBytes;
    lastPoint.raw = rawBytes;
    this.sent = Math.min(this.sent, this.series.length - 1);
    const answers = this.answers(20);
    const simpleUs = this.simpleReadUs();
    let rawTotalMs = 0;
    const rows = answers.rows.map((a) => {
      const t0 = now();
      const [count, avg] = this.raw.selectArray('SELECT count(*), avg(ms) FROM requests WHERE endpoint = ?', [a.endpoint]);
      const offset = Math.max(0, Math.ceil(0.99 * count) - 1);
      const p99 = this.raw.selectValue('SELECT ms FROM requests WHERE endpoint = ? ORDER BY ms LIMIT 1 OFFSET ?', [a.endpoint, offset]);
      const rawMs = now() - t0;
      rawTotalMs += rawMs;
      return {
        endpoint: a.endpoint,
        requests: a.requests, requestsExact: count,
        avg: a.avg, avgExact: avg, avgError: Math.abs(a.avg - avg) / avg,
        p99: a.p99, p99Exact: p99, p99Error: Math.abs(a.p99 - p99) / p99,
        rawMs,
      };
    });
    const kept = new Set(this.pc.selectArrays('SELECT ts, endpoint, ms FROM latency_anomaly').map((r) => r.join('|')));
    const spikes = { planned: 0, plannedKept: 0, yours: 0, yoursKept: 0, missedInFullMinute: 0 };
    const fullMinute = this.pc.prepare('SELECT an FROM latency_win WHERE res = 60 AND w = ? AND endpoint = ?');
    for (const s of this.scenario.planned) {
      const hit = kept.has([s.ts, s.endpoint, s.ms].join('|'));
      if (s.label === 'yours') { spikes.yours++; if (hit) spikes.yoursKept++; } else { spikes.planned++; if (hit) spikes.plannedKept++; }
      if (!hit) {
        // Counted but not kept: the minute's allowance of unusual requests was already used.
        fullMinute.bind(1, s.ts - (s.ts % 60)).bind(2, s.endpoint);
        if (fullMinute.step() && fullMinute.get(0) > 20) spikes.missedInFullMinute++;
        fullMinute.reset();
      }
    }
    fullMinute.finalize();
    const outages = this.scenario.outages.map((o) => {
      const e = ENDPOINTS[o.endpoint], from = START + o.from, to = START + o.to;
      return {
        endpoint: e, from: o.from, to: o.to, visitor: !!o.visitor,
        requests: this.raw.selectValue('SELECT count(*) FROM requests WHERE endpoint = ? AND ts >= ? AND ts < ?', [e, from, to]),
        flagged: this.pc.selectValue('SELECT coalesce(sum(an), 0) FROM latency_win WHERE res = 10 AND endpoint = ? AND w >= ? AND w < ?', [e, from, to]),
        keptWhole: this.pc.selectValue('SELECT count(*) FROM latency_anomaly WHERE endpoint = ? AND ts >= ? AND ts < ?', [e, from, to]),
      };
    });
    const tables = this.pc.selectArrays(
      `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).map((r) => r[0]);
    const tableRows = tables.map((name) => [name, this.pc.selectValue(`SELECT count(*) FROM "${name}"`)]);
    this.finished = {
      events: this.events,
      ingestMs: this.ingestMs,
      rawBytes, pcBytes, pcBytesBeforeVacuum: beforeVacuum,
      reduction: rawBytes / pcBytes,
      rows, spikes, outages, tableRows,
      readAllMs: answers.readMs, rawAllMs: rawTotalMs, simpleReadUs: simpleUs,
      rawTierRows: this.pc.selectValue('SELECT count(*) FROM latency_raw'),
      rawTierBytes: this.pc.selectValue("SELECT coalesce(sum(pgsize), 0) FROM dbstat WHERE name IN ('latency_raw', 'latency_raw_ts')"),
      anomalies: this.pc.selectValue('SELECT count(*) FROM latency_anomaly'),
      sqlite: this.sqlite3.version.libVersion,
    };
    return this.finished;
  }

  // "Ask the file": reads only.
  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    if (!/^(select|with|pragma|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) {
      return { error: 'This box only reads, one statement at a time. The file changes only through its stream.' };
    }
    try {
      const columns = [];
      const rows = [];
      const t0 = now();
      this.pc.exec({ sql: text, rowMode: 'array', resultRows: rows, columnNames: columns });
      const ms = now() - t0;
      return { columns, rows: rows.slice(0, 500), total: rows.length, ms };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    }
  }

  exportFile() {
    return this.sqlite3.capi.sqlite3_js_db_export(this.pc.pointer);
  }
}
