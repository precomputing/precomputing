// One run of Demo 3: a month of AI usage, reported by two gateways to the compiled usage policy on
// SQLite and billed from the meter's precomputes. Runs the same way in the page's worker and in
// Node, which is how the published numbers are produced.

import {
  START, MONTH_END, CLOSE, END, DISPUTE, MODELS, PLANS, CUSTOMERS, GATEWAYS, LIST, COST,
  OUTAGE, MONTH_END_DELAY, STUCK, RETRY_SHARE, Draws, mulberry32,
} from './scenario.js';

const now = () => performance.now();
const SLOW_LAG = 40 * 60;     // how late the slow eu gateway delivers around midnight at the month's end
const CUT = 12 * 3600;        // a link cut by the visitor
const STORM = 3600;           // a retry storm started by the visitor
export const SEPT = '2026-09';

export function period(ts) {
  const d = new Date(ts * 1000);
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
}

export function periodEnd(ts) {
  const d = new Date(ts * 1000);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1) / 1000;
}

// The recount: separate code that takes the reports in the order the gateways sent them and applies
// the policy's three rules itself. It never reads the meter; the results compare the two.
class Recount {
  constructor(rules) {
    this.late = rules.late_seconds;
    this.close = rules.close_seconds;
    this.clock = null;          // the newest time counted so far
    this.seen = new Set();      // request ids counted (the late rule refuses anything older than the repeat window)
    this.lines = new Map();     // period|customer|model -> {n, input, output}
    this.refused = new Map();   // reason|customer|model|gateway -> n
    this.refusedIds = new Map(); // request id -> the reason its latest report was refused
    this.hours = new Map();     // hour|customer|model|gateway -> {n, input, output}
  }

  arrive(r) {
    let reason = null;
    if (this.clock !== null && r.ts < this.clock - this.late) reason = 'late';
    else if (this.clock !== null && this.clock >= periodEnd(r.ts) + this.close) reason = 'closed';
    else if (this.seen.has(r.id)) reason = 'repeat';
    if (reason) {
      const k = `${reason}|${CUSTOMERS[r.c].id}|${MODELS[r.m]}|${r.gw}`;
      this.refused.set(k, (this.refused.get(k) || 0) + 1);
      this.refusedIds.set(r.id, reason);
      return reason;
    }
    this.seen.add(r.id);
    if (this.clock === null || r.ts > this.clock) this.clock = r.ts;
    const k = `${period(r.ts)}|${CUSTOMERS[r.c].id}|${MODELS[r.m]}`;
    let l = this.lines.get(k);
    if (!l) this.lines.set(k, (l = { n: 0, input: 0, output: 0 }));
    l.n++;
    l.input += r.input;
    l.output += r.output;
    const hk = `${r.ts - (r.ts % 3600)}|${CUSTOMERS[r.c].id}|${MODELS[r.m]}|${r.gw}`;
    let hr = this.hours.get(hk);
    if (!hr) this.hours.set(hk, (hr = { n: 0, input: 0, output: 0 }));
    hr.n++;
    hr.input += r.input;
    hr.output += r.output;
    return null;
  }
}

// Amounts in nano-dollars, computed from token counts with the same integer rules as the invoices view.
function amounts(lines, customer) {
  let list = 0n, cost = 0n;
  for (const [key, l] of lines) {
    const [, c, m] = key.split('|');
    if (c !== customer) continue;
    list += BigInt(l.input) * BigInt(LIST[m][0]) + BigInt(l.output) * BigInt(LIST[m][1]);
    cost += BigInt(l.input) * BigInt(COST[m][0]) + BigInt(l.output) * BigInt(COST[m][1]);
  }
  const plan = PLANS[CUSTOMERS.find((cu) => cu.id === customer).plan];
  const discount = (list * BigInt(plan.discountPct)) / 100n;
  return { list, discount, due: list - discount, cost };
}

export class Run {
  constructor(sqlite3, files) {
    this.sqlite3 = sqlite3;
    this.files = files; // { schema, distill, billing }
    this.reset();
  }

  reset() {
    this.close();
    const { oo1 } = this.sqlite3;
    const db = (this.db = new oo1.DB(':memory:', 'c'));
    db.exec(this.files.schema);
    db.exec(this.files.billing);
    this.rules = JSON.parse(db.selectValue("SELECT detail FROM _precomputing_objects WHERE name = 'usage'"));
    this.pageSize = db.selectValue('PRAGMA page_size');
    db.exec('BEGIN');
    for (const [id, p] of Object.entries(PLANS)) {
      db.exec({ sql: 'INSERT INTO plans (plan, name, quota, discount_pct) VALUES (?, ?, ?, ?)', bind: [id, p.name, p.quota, p.discountPct] });
    }
    for (const cu of CUSTOMERS) {
      db.exec({ sql: 'INSERT INTO customers (customer, name, plan, gateway) VALUES (?, ?, ?, ?)', bind: [cu.id, cu.name, cu.plan, cu.gateway] });
      const q = PLANS[cu.plan].quota;
      if (q != null) db.exec({ sql: 'INSERT INTO monthly_tokens_limit (customer, lim) VALUES (?, ?)', bind: [cu.id, q] });
    }
    for (const m of MODELS) {
      db.exec({ sql: 'INSERT INTO prices (model, input_nano, output_nano) VALUES (?, ?, ?)', bind: [m, ...LIST[m]] });
      db.exec({ sql: 'INSERT INTO model_costs (model, input_nano, output_nano) VALUES (?, ?, ?)', bind: [m, ...COST[m]] });
    }
    db.exec('COMMIT');
    this.ins = db.prepare('INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)');
    this.quotaSt = db.prepare('SELECT reached FROM monthly_tokens WHERE customer = ? AND period = ?');
    this.limits = new Map(CUSTOMERS.map((cu) => [cu.id, PLANS[cu.plan].quota]));

    this.t = START;               // the start of the next simulated minute
    this.draws = new Draws();
    this.stormRnd = mulberry32(601);
    this.seq = 0;                 // the order reports are handed to the gateways
    this.nextId = 0;
    this.buckets = new Map();     // minute -> reports due in it
    this.gw = Object.fromEntries(GATEWAYS.map((g) => [g, { name: g, cuts: [], held: [], waiting: 0, delivered: 0, retries: 0 }]));
    this.gw[OUTAGE.gateway].cuts.push({ from: OUTAGE.from, to: OUTAGE.to, by: 'script' });
    this.cust = CUSTOMERS.map(() => ({ reached: false, reachedAt: {}, away: {}, served: {} }));
    this.recount = new Recount(this.rules);
    this.served = 0;
    this.servedSept = [];         // ids of the requests served in September
    this.reports = 0;
    this.storm = null;
    this.stuck = { n: 0, closed: 0, late: 0, repeat: 0 };
    this.afterMidnight = { n: 0, counted: 0 };
    this.news = [];
    this.hoursSent = START;
    this.ingestMs = 0;
    this.done = false;
    this.finished = null;
  }

  close() {
    for (const s of ['ins', 'quotaSt']) {
      if (this[s]) { try { this[s].finalize(); } catch (e) { /* already closed */ } this[s] = null; }
    }
    if (this.db) { this.db.close(); this.db = null; }
  }

  say(t, kind, text) { this.news.push({ t: t - START, kind, text }); }

  down(g, time) {
    for (const c of g.cuts) if (time >= c.from && time < c.to) return c;
    return null;
  }

  // report hands one usage report to a gateway, to be delivered to the meter when it is due.
  report(r, gw, sendTs, tag) {
    let due = sendTs;
    if (gw === STUCK.gateway && sendTs >= STUCK.from && sendTs < STUCK.to) due = STUCK.at;
    else if (gw === MONTH_END_DELAY.gateway && sendTs >= MONTH_END_DELAY.from && sendTs < MONTH_END_DELAY.to) due = sendTs + SLOW_LAG;
    const it = { due, seq: this.seq++, ts: r.ts, id: r.id, c: r.c, m: r.m, input: r.input, output: r.output, gw, tag };
    const minute = due - (due % 60);
    let b = this.buckets.get(minute);
    if (!b) this.buckets.set(minute, (b = []));
    b.push(it);
    this.gw[gw].waiting++;
    if (tag) tag.sent++;
  }

  // deliver hands a report to the meter: one INSERT. The recount sees the same report.
  deliver(it, extra) {
    this.ins.bind([it.ts, it.id, CUSTOMERS[it.c].id, MODELS[it.m], it.gw, it.input, it.output]).stepReset();
    const reason = this.recount.arrive(it);
    const g = this.gw[it.gw];
    g.waiting--;
    g.delivered++;
    this.reports++;
    for (const tag of [it.tag, extra]) {
      if (!tag) continue;
      tag.reports++;
      if (reason) tag[reason] = (tag[reason] || 0) + 1;
      else tag.counted++;
    }
    if (it.due === STUCK.at && it.gw === STUCK.gateway && it.ts < MONTH_END) {
      this.stuck.n++;
      if (reason) this.stuck[reason]++;
    }
    if (it.gw === MONTH_END_DELAY.gateway && it.ts < MONTH_END && it.due >= MONTH_END && it.due < CLOSE && it.due !== STUCK.at) {
      this.afterMidnight.n++;
      if (!reason) this.afterMidnight.counted++;
    }
  }

  // deliverHeld sends everything a gateway held while its link was down, oldest first.
  deliverHeld(g, at, why) {
    const tag = { reports: 0, counted: 0 };
    const cut = g.lastCut;
    for (const it of g.held) { it.due = at; this.deliver(it, tag); }
    g.held = [];
    const refused = tag.reports - tag.counted;
    const hours = cut ? Math.round((at - cut.from) / 3600) : 0;
    const other = GATEWAYS[1 - GATEWAYS.indexOf(g.name)];
    this.say(at, 'teal', `${why || `The ${g.name} link is back after ${hours} hours.`} The gateway delivers the ${tag.reports.toLocaleString('en-US')} reports it held, `
      + `and each request counts in the hour it happened${refused ? `. Of those reports, ${this.reasons(tag, other)}` : ''}.`);
  }

  readQuota(customer, per) {
    const st = this.quotaSt;
    st.bind([customer, per]);
    const reached = st.step() ? st.get(0) === 1 : false;
    st.reset();
    return reached;
  }

  // One simulated minute: links that come back deliver what they held, each gateway reads the
  // quotas, the customers make their requests, and the reports due this minute reach the meter.
  minute() {
    const t0 = this.t;
    for (const g of Object.values(this.gw)) {
      if (g.held.length && !this.down(g, t0)) this.deliverHeld(g, t0);
    }
    const per = period(t0);
    for (let c = 0; c < CUSTOMERS.length; c++) {
      const cu = CUSTOMERS[c], st = this.cust[c];
      if (this.down(this.gw[cu.gateway], t0)) continue; // it keeps its last reading
      const reached = this.readQuota(cu.id, per);
      if (reached && !st.reached) {
        st.reachedAt[per] ??= t0;
        this.say(t0, 'amber', `${cu.name} reaches its ${PLANS[cu.plan].name} quota of ${(this.limits.get(cu.id) / 1e6).toLocaleString('en-US')} million tokens for the month. The ${cu.gateway} gateway turns its requests away.`);
      } else if (!reached && st.reached) {
        this.say(t0, 'teal', per !== period(t0 - 60) ? `A new month: ${cu.name}'s quota starts again, and the gateway serves it.` : `${cu.name} is under its quota again, and the gateway serves it.`);
      }
      st.reached = reached;
    }
    const storm = this.storm && t0 < this.storm.to ? this.storm : null;
    for (let c = 0; c < CUSTOMERS.length; c++) {
      const cu = CUSTOMERS[c], st = this.cust[c];
      for (const q of this.draws.minute(c, t0, RETRY_SHARE)) {
        if (st.reached) { st.away[per] = (st.away[per] || 0) + 1; continue; }
        const r = { ts: q.ts, id: 'r' + (this.nextId++).toString(36).padStart(6, '0'), c, m: q.model, input: q.input, output: q.output };
        this.served++;
        st.served[per] = (st.served[per] || 0) + 1;
        if (per === SEPT) this.servedSept.push(r.id);
        const tag = storm && q.ts >= storm.from ? storm : null;
        if (tag) tag.requests++;
        this.report(r, cu.gateway, q.ts, tag);
        if (q.retryAfter) {
          const other = GATEWAYS[1 - GATEWAYS.indexOf(cu.gateway)];
          const via = q.retryOther ? other : cu.gateway;
          this.gw[via].retries++;
          this.report(r, via, q.ts + q.retryAfter, tag);
        }
        if (tag) {
          for (let k = 0; k < 2; k++) {
            const via = GATEWAYS[this.stormRnd() < 0.5 ? 0 : 1];
            this.gw[via].retries++;
            this.report(r, via, q.ts + 1 + Math.floor(this.stormRnd() * 30), tag);
          }
        }
      }
    }
    const b = this.buckets.get(t0);
    if (b) {
      this.buckets.delete(t0);
      b.sort((x, y) => x.due - y.due || x.seq - y.seq);
      for (const it of b) {
        const g = this.gw[it.gw];
        const cut = this.down(g, it.due);
        if (cut) { g.held.push(it); g.lastCut = cut; } else this.deliver(it);
      }
    }
    this.t = t0 + 60;
    this.scripted(t0);
    if (this.t % 3600 === 0) this.distill();
    if (this.t >= END) this.done = true;
  }

  // reasons describes the refusals among the reports a gateway held.
  reasons(tag, other) {
    const n = (x) => x.toLocaleString('en-US');
    const parts = [];
    if (tag.closed) parts.push(`${n(tag.closed)} ${tag.closed === 1 ? 'was' : 'were'} refused because ${tag.closed === 1 ? 'its' : 'their'} month had closed`);
    if (tag.late) parts.push(`${n(tag.late)} ${tag.late === 1 ? 'was' : 'were'} refused as more than two days late`);
    if (tag.repeat) parts.push(`${n(tag.repeat)} had already reached the meter as retries through the ${other} gateway and ${tag.repeat === 1 ? 'was' : 'were'} refused as ${tag.repeat === 1 ? 'a repeat' : 'repeats'}`);
    return parts.join('; ');
  }

  // Lines for the log that carry numbers only the run knows.
  scripted(t0) {
    if (t0 === STUCK.at) {
      const s = this.stuck;
      this.say(t0, 'red', `The stuck us queue is emptied: ${s.n.toLocaleString('en-US')} reports of requests made between 18:00 and 19:00 on 30 September. `
        + `September closed at midnight, so the meter refuses ${s.closed === s.n ? 'them all' : `${s.closed.toLocaleString('en-US')} of them`} and counts them in usage_refused. The September invoices do not change.`);
    }
    if (t0 === MONTH_END_DELAY.to + SLOW_LAG && this.afterMidnight.n) {
      const a = this.afterMidnight;
      this.say(t0, 'teal', `The eu gateway has caught up. ${a.counted.toLocaleString('en-US')} September requests reported after midnight counted in September, when they happened.`);
    }
    const s = this.storm;
    if (s && !s.reported && t0 >= s.to && s.reports === s.sent) {
      s.reported = true;
      this.say(t0, 'teal', `Your retry storm is over: ${s.reports.toLocaleString('en-US')} reports for ${s.requests.toLocaleString('en-US')} requests. `
        + `${s.counted.toLocaleString('en-US')} counted, ${(s.repeat || 0).toLocaleString('en-US')} refused as repeats. Each request is billed once.`);
    }
  }

  // Detail that has outlived the policy fades: run hourly with the time of the newest request.
  distill(at) {
    const clock = at ?? this.db.selectValue('SELECT newest FROM usage_clock WHERE g = 1');
    if (clock != null) this.db.exec(this.files.distill.replaceAll(':now', String(clock)));
  }

  // Simulated seconds until maxSeconds are done or budgetMs has passed.
  step(maxSeconds, budgetMs = 60) {
    if (this.done) return 0;
    const t0 = now();
    const until = this.t + maxSeconds;
    let secs = 0;
    this.db.exec('BEGIN');
    try {
      while (this.t + 60 <= until && now() - t0 < budgetMs && !this.done) {
        this.minute();
        secs += 60;
      }
    } finally {
      this.db.exec('COMMIT');
    }
    this.ingestMs += now() - t0;
    return secs;
  }

  // Visitor actions
  retryStorm() {
    if (this.storm && this.t < this.storm.to) return null;
    this.storm = { from: this.t, to: this.t + STORM, requests: 0, sent: 0, reports: 0, counted: 0, reported: false };
    this.say(this.t, 'amber', 'Your retry storm: for the next hour, every request is reported three times, through both gateways.');
    return this.storm;
  }

  cutLink(gw) {
    const g = this.gw[gw];
    const cur = this.down(g, this.t);
    if (cur) { cur.to = Math.max(cur.to, this.t + CUT); cur.by = 'you'; g.lastCut = cur; }
    else { const cut = { from: this.t, to: this.t + CUT, by: 'you' }; g.cuts.push(cut); g.lastCut = cut; }
    this.say(this.t, 'red', `You cut the ${gw} gateway's link to the meter for 12 hours. It keeps serving its customers and holds their reports.`);
  }

  raiseQuota(customer) {
    const lim = this.limits.get(customer);
    if (lim == null) return;
    this.limits.set(customer, lim * 2);
    this.db.exec({ sql: 'UPDATE monthly_tokens_limit SET lim = ? WHERE customer = ?', bind: [lim * 2, customer] });
    const cu = CUSTOMERS.find((x) => x.id === customer);
    this.say(this.t, 'amber', `You doubled ${cu.name}'s quota to ${(lim * 2 / 1e6).toLocaleString('en-US')} million tokens a month. The gateway reads it within a minute.`);
  }

  // Stretches the page plays in slow motion, so that a gap can be seen before it fills in.
  slowMotion() {
    const w = [
      [OUTAGE.from - 3600, OUTAGE.to + 3600],
      [STUCK.from - 1800, STUCK.from + 1800],
      [MONTH_END_DELAY.from - 1800, MONTH_END_DELAY.to + SLOW_LAG + 1800],
      [CLOSE - 1800, CLOSE + 1800],
      [STUCK.at - 1800, END],
    ];
    for (const g of Object.values(this.gw)) for (const c of g.cuts) if (c.by === 'you') w.push([c.from, c.to + 3600]);
    if (this.storm) w.push([this.storm.from, this.storm.to + 1800]);
    return w.some(([a, b]) => this.t >= a && this.t < b);
  }

  bytes(db = this.db, free = true) {
    const pages = db.selectValue('PRAGMA page_count') - (free ? db.selectValue('PRAGMA freelist_count') : 0);
    return pages * this.pageSize;
  }

  gateways() {
    return GATEWAYS.map((name) => {
      const g = this.gw[name];
      const cut = this.down(g, this.t);
      const slow = name === MONTH_END_DELAY.gateway && this.t > MONTH_END_DELAY.from && this.t <= MONTH_END_DELAY.to + SLOW_LAG;
      const stuck = name === STUCK.gateway && this.t > STUCK.from && this.t <= STUCK.at;
      return {
        name, held: g.held.length, waiting: g.waiting, delivered: g.delivered, retries: g.retries,
        down: cut ? { from: cut.from - START, to: cut.to - START, by: cut.by } : null, slow, stuck,
        customers: CUSTOMERS.filter((c) => c.gateway === name).map((c) => c.name),
      };
    });
  }

  bands() {
    const out = [];
    for (const name of GATEWAYS) {
      for (const c of this.gw[name].cuts) if (c.from < this.t) out.push({ gw: name, kind: 'cut', by: c.by, from: c.from - START, to: Math.min(c.to, this.t) - START });
    }
    if (this.t > STUCK.from) out.push({ gw: STUCK.gateway, kind: 'stuck', from: STUCK.from - START, to: Math.min(STUCK.to, this.t) - START });
    return out;
  }

  // Hours that can still change: those within the late window of the meter's clock, complete ones only.
  // Hours already sent are sent again while a late report can still change them.
  hours() {
    const clock = this.db.selectValue('SELECT newest FROM usage_clock WHERE g = 1') ?? START;
    const lateFrom = Math.floor((Math.min(clock, this.t) - this.rules.late_seconds) / 3600) * 3600 - 3600;
    const from = Math.max(START, Math.min(this.hoursSent, lateFrom));
    const to = this.t - (this.t % 3600);
    this.hoursSent = to;
    const rows = this.db.selectArrays('SELECT w, gateway, sum(n) FROM usage_win WHERE res = 3600 AND w >= ? AND w < ? GROUP BY w, gateway', [from, to]);
    const by = new Map();
    for (let w = from; w < to; w += 3600) by.set(w, { t: w - START + 1800, eu: 0, us: 0 });
    for (const [w, g, n] of rows) by.get(w)[g] = n;
    return [...by.values()];
  }

  snapshot() {
    const db = this.db;
    const inv = db.selectObjects('SELECT period, customer, requests, input_tokens, output_tokens, list_nano, discount_nano, due_nano, cost_nano FROM invoices');
    const quotas = db.selectObjects('SELECT customer, period, used, lim, reached FROM monthly_tokens');
    const refused = Object.fromEntries(['repeat', 'late', 'closed'].map((r) => [r, 0]));
    for (const [reason, n] of db.selectArrays('SELECT reason, sum(n) FROM usage_refused GROUP BY reason')) refused[reason] = n;
    const news = this.news;
    this.news = [];
    return {
      t: this.t - START,
      duration: END - START,
      served: this.served,
      reports: this.reports,
      metered: db.selectValue('SELECT coalesce(sum(n), 0) FROM _pc_usage_by_customer_per_month'),
      refused,
      invoices: inv,
      quotas,
      customers: this.cust.map((st, i) => ({ id: CUSTOMERS[i].id, reached: st.reached, reachedAt: st.reachedAt, away: st.away, served: st.served, limit: this.limits.get(CUSTOMERS[i].id) })),
      gateways: this.gateways(),
      bands: this.bands(),
      hours: this.hours(),
      fileBytes: this.bytes(),
      ingestMs: this.ingestMs,
      storm: this.storm && this.t < this.storm.to,
      slow: this.slowMotion(),
      news,
      done: this.done,
    };
  }

  // A copy of the file, for the dispute window.
  copy() {
    const { sqlite3 } = this;
    const bytes = sqlite3.capi.sqlite3_js_db_export(this.db.pointer);
    const db = new sqlite3.oo1.DB(':memory:', 'c');
    const p = sqlite3.wasm.allocFromTypedArray(bytes);
    const rc = sqlite3.capi.sqlite3_deserialize(db.pointer, 'main', p, bytes.byteLength, bytes.byteLength,
      sqlite3.capi.SQLITE_DESERIALIZE_FREEONCLOSE | sqlite3.capi.SQLITE_DESERIALIZE_RESIZEABLE);
    db.checkRc(rc);
    return db;
  }

  tables(db) {
    const rows = db.selectArrays(`SELECT name, sum(pgsize) FROM dbstat GROUP BY name ORDER BY name`);
    const kinds = new Map(db.selectArrays("SELECT name, type FROM sqlite_master"));
    return rows.filter(([name]) => !name.startsWith('sqlite_')).map(([name, bytes]) => ({
      name, bytes, rows: kinds.get(name) === 'table' ? db.selectValue(`SELECT count(*) FROM "${name}"`) : null,
    }));
  }

  // At the end of the run the gateways deliver whatever they still hold or have waiting.
  flush() {
    this.db.exec('BEGIN');
    try {
      for (const g of Object.values(this.gw)) if (g.held.length) this.deliverHeld(g, END, `The run ends while the ${g.name} link is down.`);
      for (const minute of [...this.buckets.keys()].sort((a, b) => a - b)) {
        const b = this.buckets.get(minute);
        b.sort((x, y) => x.due - y.due || x.seq - y.seq);
        for (const it of b) this.deliver(it);
      }
      this.buckets.clear();
    } finally {
      this.db.exec('COMMIT');
    }
  }

  // Everything the results section shows, measured after the last report.
  finish() {
    if (this.finished) return this.finished;
    while (!this.done) this.step(1e12, 1e9);
    this.flush();
    this.distill();
    const db = this.db;
    const rec = this.recount;

    // 1. Invoice lines against the recount and against the requests kept whole in the file.
    const lines = db.selectArrays('SELECT customer, model, requests, input_tokens, output_tokens FROM invoice_lines WHERE period = ? ORDER BY customer, model', [SEPT]);
    const fromRaw = new Map(db.selectArrays(
      'SELECT customer, model, count(*), sum(input_tokens), sum(output_tokens) FROM usage_raw WHERE ts >= ? AND ts < ? GROUP BY customer, model', [START, MONTH_END])
      .map((r) => [`${r[0]}|${r[1]}`, r.slice(2)]));
    const fromRecount = new Map([...rec.lines].filter(([k]) => k.startsWith(SEPT + '|')).map(([k, l]) => [k.slice(SEPT.length + 1), [l.n, l.input, l.output]]));
    const same = (a, b) => a && b && a.length === b.length && a.every((x, i) => x === b[i]);
    let recountDiffs = Math.abs(fromRecount.size - lines.length), rawDiffs = Math.abs(fromRaw.size - lines.length);
    for (const [c, m, n, i, o] of lines) {
      if (!same([n, i, o], fromRecount.get(`${c}|${m}`))) recountDiffs++;
      if (!same([n, i, o], fromRaw.get(`${c}|${m}`))) rawDiffs++;
    }

    // 2. Amounts due, to the nano-dollar: the invoices view against integer arithmetic on the recount.
    const inv = db.selectObjects('SELECT customer, name, plan, requests, input_tokens, output_tokens, list_nano, discount_nano, due_nano, due_cents, cost_nano FROM invoices WHERE period = ? ORDER BY due_nano DESC, customer', [SEPT]);
    const recLines = new Map([...rec.lines].filter(([k]) => k.startsWith(SEPT + '|')));
    let amountDiffs = 0;
    const invoices = inv.map((r) => {
      const a = amounts(recLines, r.customer);
      const ok = BigInt(r.list_nano) === a.list && BigInt(r.due_nano) === a.due && BigInt(r.cost_nano) === a.cost;
      if (!ok) amountDiffs++;
      return { ...r, ok };
    });

    // 3. Refusals, by reason, customer, model and gateway.
    const refusedRows = db.selectArrays('SELECT reason, customer, model, gateway, n FROM usage_refused');
    let refusedDiffs = Math.abs(refusedRows.length - rec.refused.size);
    const refused = { repeat: 0, late: 0, closed: 0 };
    for (const [reason, c, m, g, n] of refusedRows) {
      refused[reason] += n;
      if (rec.refused.get(`${reason}|${c}|${m}|${g}`) !== n) refusedDiffs++;
    }

    // 4. The hourly windows: every late report in the hour its request happened.
    const hourRows = db.selectArrays('SELECT w, customer, model, gateway, n, input_tokens_sum, output_tokens_sum FROM usage_win WHERE res = 3600');
    let hourDiffs = Math.abs(hourRows.length - rec.hours.size);
    for (const [w, c, m, g, n, i, o] of hourRows) {
      const x = rec.hours.get(`${w}|${c}|${m}|${g}`);
      if (!x || x.n !== n || x.input !== i || x.output !== o) hourDiffs++;
    }

    // 5. Every request served in September: billed once, or refused with a reason.
    let billed = 0;
    const notBilled = { closed: 0, late: 0, repeat: 0, never: 0 };
    for (const id of this.servedSept) {
      if (rec.seen.has(id)) billed++;
      else notBilled[rec.refusedIds.get(id) || 'never']++;
    }
    const [rawRows, rawDistinct] = db.selectArray('SELECT count(*), count(DISTINCT request_id) FROM usage_raw WHERE ts >= ? AND ts < ?', [START, MONTH_END]);
    const meterSept = db.selectValue('SELECT coalesce(sum(n), 0) FROM _pc_usage_by_customer_per_month WHERE period = ?', [SEPT]);

    // 6. Reading the invoices: from the precomputes, and from every request. A quota check.
    const reps = 50;
    let t0 = now();
    for (let k = 0; k < reps; k++) db.selectArrays('SELECT customer, due_nano FROM invoices WHERE period = ?', [SEPT]);
    const invoiceMs = (now() - t0) / reps;
    t0 = now();
    const slow = db.selectArrays(`SELECT r.customer, sum(r.input_tokens * p.input_nano + r.output_tokens * p.output_nano)
      FROM usage_raw r JOIN prices p USING (model) WHERE r.ts >= ? AND r.ts < ? GROUP BY r.customer`, [START, MONTH_END]);
    const rawInvoiceMs = now() - t0;
    const slowOk = slow.every(([c, list]) => inv.some((r) => r.customer === c && r.list_nano === list));
    const checks = 20000;
    t0 = now();
    for (let k = 0; k < checks; k++) this.readQuota(CUSTOMERS[k % CUSTOMERS.length].id, SEPT);
    const quotaUs = ((now() - t0) / checks) * 1000;

    // 7. The file now, and after the dispute window: a copy distilled as on 31 December.
    const fileBytes = this.bytes(db, false);
    const tablesNow = this.tables(db);
    const copy = this.copy();
    const disputeEnd = CLOSE + DISPUTE;
    copy.exec(this.files.distill.replaceAll(':now', String(disputeEnd)));
    copy.exec('VACUUM');
    const laterBytes = this.bytes(copy, false);
    const tablesLater = this.tables(copy);
    const later = copy.selectObjects('SELECT customer, list_nano, due_nano, cost_nano FROM invoices WHERE period = ?', [SEPT]);
    const laterSame = later.length === inv.length && later.every((r) => inv.some((x) => x.customer === r.customer && x.list_nano === r.list_nano && x.due_nano === r.due_nano && x.cost_nano === r.cost_nano));
    const laterRaw = copy.selectValue('SELECT count(*) FROM usage_raw WHERE ts < ?', [MONTH_END]);
    const laterOct = copy.selectValue('SELECT count(*) FROM usage_raw WHERE ts >= ?', [MONTH_END]);
    copy.close();

    const totals = inv.reduce((a, r) => ({ due: a.due + BigInt(r.due_nano), cost: a.cost + BigInt(r.cost_nano), list: a.list + BigInt(r.list_nano) }), { due: 0n, cost: 0n, list: 0n });
    this.finished = {
      served: this.served,
      servedSept: this.servedSept.length,
      billed, notBilled, meterSept, rawRows, rawDistinct,
      reports: this.reports,
      ingestMs: this.ingestMs,
      lines: lines.length, recountDiffs, rawDiffs,
      invoices, amountDiffs,
      due: totals.due.toString(), cost: totals.cost.toString(), list: totals.list.toString(),
      refusedRows: refusedRows.length, refusedDiffs, refused,
      stuck: this.stuck,
      afterMidnight: this.afterMidnight,
      away: this.cust.map((st, i) => ({ id: CUSTOMERS[i].id, sept: st.away[SEPT] || 0, reachedAt: st.reachedAt[SEPT] ?? null })),
      invoiceMs, rawInvoiceMs, slowOk, quotaUs,
      hourRows: hourRows.length, hourDiffs,
      fileBytes, tablesNow,
      laterBytes, tablesLater, laterSame, laterRaw, laterOct, disputeEnd,
      sqlite: this.sqlite3.version.libVersion,
    };
    return this.finished;
  }

  // "Ask the meter": reads only.
  query(sql) {
    const text = sql.trim().replace(/;+\s*$/, '');
    if (!/^(select|with|pragma|explain|values)\b/i.test(text) || /;\s*\S/.test(text)) {
      return { error: 'This box only reads, one statement at a time. The meter changes only through its stream.' };
    }
    try {
      const columns = [];
      const rows = [];
      const t0 = now();
      this.db.exec({ sql: text, rowMode: 'array', resultRows: rows, columnNames: columns });
      const ms = now() - t0;
      return { columns, rows: rows.slice(0, 500), total: rows.length, ms };
    } catch (e) {
      return { error: String(e.message || e).replace(/^SQLITE_ERROR: sqlite3 result code 1: /, '') };
    }
  }

  exportFile() {
    return this.sqlite3.capi.sqlite3_js_db_export(this.db.pointer);
  }
}
