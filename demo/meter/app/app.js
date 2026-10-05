// Demo 3 page: controls, customers, gateways, invoices, the chart and the SQL box. The meter runs in worker.js.
import { $, h, fmt, LineChart, renderResult, highlight, loadCompiler, isEmbedded } from '../../lib/kit.js';
import { START, MONTH_END, CLOSE, END, CUSTOMERS, PLANS, OUTAGE, MONTH_END_DELAY, STUCK } from './scenario.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });
const SEPT = '2026-09', OCT = '2026-10';

let state = 'loading';      // loading, idle, playing, paused, finishing, done
let files = null;
let last = null;
let started = false;
let queryId = 0;
const logged = new Set();
const hours = new Map();     // hour -> {t, eu, us}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const hm = (d) => `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
const dayTime = (ts) => { const d = new Date(ts * 1000); return `${DAYS[d.getUTCDay()]} ${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}, ${hm(d)}`; };
const shortTime = (ts) => { const d = new Date(ts * 1000); return `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${hm(d)}`; };
const dayLabel = (ts) => { const d = new Date(ts * 1000); return `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}`; };

// Money arrives in billionths of a dollar; it is rounded to the cent only for display.
function usd(nano) {
  const cents = (BigInt(nano ?? 0) + 5000000n) / 10000000n;
  const d = cents / 100n, c = cents % 100n;
  return `$${Number(d).toLocaleString('en-US')}.${String(c).padStart(2, '0')}`;
}
const tokens = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${Math.round(n / 1e3)}k` : fmt.int(n));
const pct = (x) => (x == null || !isFinite(x) ? '–' : `${(x * 100).toFixed(1)}%`);

const DAY = 86400;
let hourView = '3d';
const hourChart = (el, name, color) => new LineChart(el, {
  height: 150, top: 10, xMin: 0, xMax: 3 * DAY, xTicks: [], right: 120,
  xLabel: (t) => dayLabel(START + t),
  tipLabel: (t) => `${dayTime(START + t - 1800)} to ${hm(new Date((START + t + 1800) * 1000))}`,
  yFormat: (v, precise) => (precise ? `${fmt.int(v)} an hour` : fmt.int(v)),
  series: [{ key: 'n', name, color }],
  aria: `Line chart of requests an hour through the ${name}.`,
});
const charts = { eu: hourChart($('eu-chart'), 'eu gateway', 'var(--teal)'), us: hourChart($('us-chart'), 'us gateway', 'var(--navy-2)') };

function drawHours() {
  const t = last ? last.t : 0;
  let xMin, xMax, ticks = [];
  if (hourView === 'month') {
    xMin = 0; xMax = END - START;
    ticks = [0, 7 * DAY, 14 * DAY, 21 * DAY, 28 * DAY];
  } else {
    xMax = Math.max(3 * DAY, t);
    xMin = xMax - 3 * DAY;
    for (let d = Math.ceil(xMin / DAY) * DAY; d < xMax; d += DAY) ticks.push(d);
  }
  const pts = [...hours.values()].filter((p) => p.t >= xMin && p.t <= xMax).sort((a, b) => a.t - b.t);
  for (const [gw, chart] of Object.entries(charts)) {
    Object.assign(chart.o, { xMin, xMax, xTicks: ticks });
    const bands = (last ? last.bands : []).filter((b) => b.gw === gw && b.to > xMin && b.from < xMax).map((b) => ({
      from: Math.max(b.from, xMin), to: Math.min(b.to, xMax),
      color: b.kind === 'stuck' ? 'var(--amber)' : 'var(--red)', opacity: b.kind === 'stuck' ? 0.3 : 0.2 }));
    chart.setData(pts.map((p) => ({ t: p.t, n: p[gw] })), bands);
  }
}
function setHourView(v) {
  hourView = v;
  $('view-3d').setAttribute('aria-pressed', String(v === '3d'));
  $('view-month').setAttribute('aria-pressed', String(v === 'month'));
  drawHours();
}
$('view-3d').addEventListener('click', () => setHourView('3d'));
$('view-month').addEventListener('click', () => setHourView('month'));

// Customer cards
const cards = new Map();
for (const cu of CUSTOMERS) {
  const el = {
    due: h('b', { text: '$0.00' }),
    meta: h('div', { class: 'meta' }),
    bar: h('i'),
    quota: h('div', { class: 'meta' }),
    away: h('div', { class: 'meta away', hidden: true }),
    oct: h('div', { class: 'meta', hidden: true }),
  };
  const plan = PLANS[cu.plan];
  const card = h('div', { class: `cust plan-${cu.plan}` },
    h('div', { class: 'top' }, h('span', { class: 'nm', text: cu.name }), h('span', { class: 'plan', text: `${plan.name} · ${cu.gateway}` })),
    h('div', { class: 'amt' }, el.due, h('span', { text: ' due for September' })),
    el.meta,
    h('div', { class: `qbar${plan.quota == null ? ' none' : ''}` }, el.bar),
    el.quota, el.away, el.oct);
  cards.set(cu.id, el);
  $('custs').append(card);
}

function setState(s) {
  state = s;
  const busy = s === 'loading' || s === 'finishing';
  const live = s === 'playing' || s === 'paused';
  $('btn-play').disabled = busy;
  $('play-label').textContent = s === 'playing' ? 'Pause' : s === 'paused' ? 'Resume' : s === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', s === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || s === 'idle';
  $('btn-review').disabled = busy || s === 'done';
  $('btn-storm').disabled = !live || !!(last && last.storm);
  $('btn-cut').disabled = !live;
  $('btn-quota').disabled = !live || !juniperReached();
  $('sql-run').disabled = !(s === 'paused' || s === 'done' || s === 'idle');
  $('sql-status').textContent = s === 'playing' ? 'Pause the month to ask the meter.' : '';
  const hints = {
    idle: 'Press Play. September passes in about a minute, slowing down when something happens. Start a retry storm or cut a link as it runs.',
    playing: 'Try a retry storm or cut the eu link: each request is still billed exactly once.',
    paused: 'Paused. You can ask the meter now, or resume.',
    finishing: 'Finishing the month at full speed, then checking every invoice.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = hints[s] || '';
  if (live && juniperReached()) $('hint').textContent = 'Juniper Bakery Blog has used its Free quota for the month. Raise it, or let the gateway keep turning its requests away.';
}

function juniperReached() {
  const c = last && last.customers.find((x) => x.id === 'juniper');
  return !!(c && c.reached);
}

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) { started = true; log(0, 'September starts: six customers call three models through the eu and us gateways.', 'teal'); }
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) { started = true; log(0, 'September starts. Running the month at full speed.', 'teal'); }
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('btn-storm').addEventListener('click', () => { worker.postMessage({ type: 'storm' }); $('btn-storm').disabled = true; });
$('btn-cut').addEventListener('click', () => worker.postMessage({ type: 'cut', gateway: 'eu' }));
$('btn-quota').addEventListener('click', () => { worker.postMessage({ type: 'quota', customer: 'juniper' }); $('btn-quota').disabled = true; });

function resetView() {
  logged.clear();
  hours.clear();
  started = false;
  last = null;
  $('log').replaceChildren();
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  $('c-rate').textContent = '–';
  drawHours();
}

// log adds a line in time order, newest first, whenever it arrives.
function log(t, text, kind = '') {
  const li = h('li', { 'data-t': t }, h('span', { class: 'when', text: shortTime(START + t) }), h('span', { class: `dot ${kind}` }), h('span', { text }));
  const after = [...$('log').children].find((x) => Number(x.dataset.t) <= t);
  $('log').insertBefore(li, after || null);
}

// Scripted moments, logged as the clock passes them. Lines with numbers come from the worker.
const script = [
  { t: OUTAGE.from - START, text: 'The eu gateway loses its link to the meter. It keeps serving its customers and holds their reports.', kind: 'red' },
  { t: STUCK.from - START, text: 'A queue in the us gateway gets stuck. Nobody notices; its reports wait there.', kind: 'amber' },
  { t: MONTH_END_DELAY.from - START, text: 'The eu gateway slows down: its reports reach the meter 40 minutes late.', kind: 'amber' },
  { t: MONTH_END - START, text: 'September ends. Reports of September requests still count in September until the month closes, a day from now.', kind: 'teal' },
  { t: CLOSE - START, text: 'September closes. Its invoices are final: the meter refuses anything that would change them.', kind: 'teal' },
];

function onSnapshot(m) {
  last = m;
  const t = m.t;
  $('bar').style.width = `${(100 * t) / m.duration}%`;
  $('clock').textContent = `${dayTime(START + t)} UTC${m.slow && state === 'playing' && !m.done ? ' · slow motion' : ''}`;
  $('c-metered').textContent = fmt.int(m.metered);
  $('c-repeat').textContent = fmt.int(m.refused.repeat);
  if (m.ingestMs > 50 && m.reports) $('c-rate').textContent = fmt.int(m.reports / (m.ingestMs / 1000));
  $('c-file').textContent = fmt.bytes(m.fileBytes);

  const sept = m.invoices.filter((r) => r.period === SEPT);
  const oct = new Map(m.invoices.filter((r) => r.period === OCT).map((r) => [r.customer, r]));
  const quota = new Map(m.quotas.map((q) => [`${q.period}|${q.customer}`, q]));
  let due = 0n, cost = 0n;
  for (const r of sept) { due += BigInt(r.due_nano); cost += BigInt(r.cost_nano); }
  $('c-revenue').textContent = usd(due);
  $('c-margin').textContent = due > 0n ? pct(1 - Number(cost) / Number(due)) : '–';

  // Customer cards
  for (const cu of CUSTOMERS) {
    const el = cards.get(cu.id);
    const inv = sept.find((r) => r.customer === cu.id);
    const st = m.customers.find((c) => c.id === cu.id);
    const used = inv ? inv.input_tokens + inv.output_tokens : 0;
    el.due.textContent = usd(inv ? inv.due_nano : 0);
    el.meta.textContent = `${fmt.int(inv ? inv.requests : 0)} requests · ${tokens(used)} tokens · model cost ${usd(inv ? inv.cost_nano : 0)}`;
    const q = quota.get(`${SEPT}|${cu.id}`);
    const lim = q ? q.lim : st.limit;
    if (lim == null) {
      el.bar.style.width = '0';
      el.quota.textContent = 'No quota on this plan';
    } else {
      el.bar.style.width = `${Math.min(100, (100 * used) / lim)}%`;
      el.bar.parentElement.classList.toggle('full', used >= lim);
      const at = st.reachedAt[SEPT];
      el.quota.textContent = `${tokens(used)} of ${tokens(lim)} tokens${at ? ` · reached ${shortTime(at)}` : ''}`;
    }
    const away = st.away[SEPT] || 0;
    el.away.hidden = !away;
    el.away.textContent = `${fmt.int(away)} requests turned away at the gateway`;
    const o = oct.get(cu.id);
    el.oct.hidden = t < MONTH_END - START;
    const oq = quota.get(`${OCT}|${cu.id}`);
    el.oct.textContent = `October so far: ${fmt.int(o ? o.requests : 0)} requests · ${tokens(o ? o.input_tokens + o.output_tokens : 0)}${oq ? ` of ${tokens(oq.lim)}` : ''} tokens`;
  }
  $('cust-sub').textContent = t >= CLOSE - START
    ? 'September, closed: these amounts are final. Read from the meter; the quota bar is the monthly_tokens view.'
    : 'September so far, read from the meter as reports arrive. The quota bar is the monthly_tokens view, which the gateways read once a minute.';

  // Gateways
  $('gateways').tBodies[0].replaceChildren(...m.gateways.map((g) => {
    let link;
    if (g.down) link = h('span', { class: 'bad', text: `Down, holding ${fmt.int(g.held)}`, title: `Down since ${shortTime(START + g.down.from)}` });
    else if (g.slow) link = h('span', { class: 'warn-text', text: 'Slow, 40 min behind' });
    else if (g.stuck) link = h('span', { class: 'warn-text', text: 'Up, a queue is stuck' });
    else link = h('span', { class: 'ok', text: 'Up' });
    return h('tr', {},
      h('td', {}, h('code', { text: g.name })),
      h('td', {}, link),
      h('td', { class: 'num', text: fmt.int(g.delivered) }),
      h('td', { class: 'num', text: fmt.int(g.waiting) }),
      h('td', { class: 'num', text: fmt.int(g.retries) }));
  }));

  // Refusals
  const reasons = [
    ['repeat', 'A request already counted, reported again: a retry'],
    ['late', 'More than two days late'],
    ['closed', 'Its month had already closed'],
  ];
  $('refused').tBodies[0].replaceChildren(...reasons.map(([r, what]) => h('tr', {},
    h('td', {}, h('code', { text: r })), h('td', { text: what }), h('td', { class: 'num', text: fmt.int(m.refused[r]) }))));
  const away = m.customers.reduce((a, c) => a + Object.values(c.away).reduce((x, y) => x + y, 0), 0);
  $('away-note').textContent = away
    ? `The gateways also turned away ${fmt.int(away)} requests over quota. Those were never served, so the meter never saw them.`
    : 'Requests over quota are turned away at the gateway, never served, and so never reach the meter.';

  // Invoices
  const rows = [...sept].sort((a, b) => b.due_nano - a.due_nano || b.list_nano - a.list_nano);
  const margin = (r) => (Number(r.due_nano) > 0 ? pct(1 - Number(r.cost_nano) / Number(r.due_nano)) : '–');
  const name = (id) => CUSTOMERS.find((c) => c.id === id).name;
  const plan = (id) => PLANS[CUSTOMERS.find((c) => c.id === id).plan];
  $('invoices').tBodies[0].replaceChildren(...rows.map((r) => h('tr', {},
    h('td', { text: name(r.customer) }),
    h('td', { text: plan(r.customer).name }),
    h('td', { class: 'num', text: fmt.int(r.requests) }),
    h('td', { class: 'num', text: fmt.int(r.input_tokens) }),
    h('td', { class: 'num', text: fmt.int(r.output_tokens) }),
    h('td', { class: 'num', text: usd(r.list_nano) }),
    h('td', { class: 'num', text: plan(r.customer).discountPct ? `−${usd(r.discount_nano)}` : '' }),
    h('td', { class: 'num strong', text: usd(r.due_nano) }),
    h('td', { class: 'num', text: usd(r.cost_nano) }),
    h('td', { class: 'num', text: margin(r) }))));
  const sum = (k) => sept.reduce((a, r) => a + BigInt(r[k]), 0n);
  $('invoices').tFoot.replaceChildren(sept.length ? h('tr', {},
    h('td', { text: 'All customers' }), h('td'),
    h('td', { class: 'num', text: fmt.int(Number(sum('requests'))) }),
    h('td', { class: 'num', text: fmt.int(Number(sum('input_tokens'))) }),
    h('td', { class: 'num', text: fmt.int(Number(sum('output_tokens'))) }),
    h('td', { class: 'num', text: usd(sum('list_nano')) }),
    h('td', { class: 'num', text: `−${usd(sum('discount_nano'))}` }),
    h('td', { class: 'num strong', text: usd(due) }),
    h('td', { class: 'num', text: usd(cost) }),
    h('td', { class: 'num', text: due > 0n ? pct(1 - Number(cost) / Number(due)) : '–' })) : '');
  const closed = t >= CLOSE - START;
  $('inv-title').textContent = closed ? 'September invoices, final' : 'September invoices, so far';

  // Chart
  for (const p of m.hours) hours.set(p.t, p);
  drawHours();

  for (const n of m.news) log(n.t, n.text, n.kind);
  if (started) {
    for (const s of script) if (t > s.t && !logged.has(s.t)) { logged.add(s.t); log(s.t, s.text, s.kind); }
  }
  if (m.done && !logged.has('done')) { logged.add('done'); log(m.duration, 'The run ends at 06:00 on 2 October. The results are below.', 'teal'); }
  if (state === 'playing' || state === 'paused') setState(state);
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const perSec = r.reports / (r.ingestMs / 1000);
  $('c-rate').textContent = fmt.int(perSec);
  $('c-file').textContent = fmt.bytes(r.fileBytes);
  const due = BigInt(r.due), cost = BigInt(r.cost);
  const notBilled = r.servedSept - r.billed;
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.servedSept)} requests served in September, ${fmt.int(r.billed)} billed, each exactly once. ` }),
    `The meter refused ${fmt.int(r.refused.repeat)} retried reports as repeats`,
    r.refused.closed ? ` and ${fmt.int(r.refused.closed)} reports that arrived after September closed` : '',
    r.refused.late ? `, plus ${fmt.int(r.refused.late)} more than two days late` : '',
    '. ',
    `The invoices come to ${usd(due)} against ${usd(cost)} of model cost, a ${pct(1 - Number(cost) / Number(due))} margin, and they match a separate recount to the billionth of a dollar. `,
    `This device took the reports in at about ${fmt.int(perSec)} a second, with every one going through the compiled triggers.`);
  const ok = (d, text) => h('span', { class: d ? 'bad' : 'ok', text: d ? `${d} differ` : text || 'identical' });
  const nb = r.notBilled;
  const lostText = [nb.closed ? `${fmt.int(nb.closed)} refused, month closed` : '', nb.late ? `${fmt.int(nb.late)} refused, late` : '', nb.never ? `${fmt.int(nb.never)} never delivered` : '']
    .filter(Boolean).join(', ');
  const rows = [
    ['Invoice lines (requests, tokens in, tokens out) against the recount', r.lines, ok(r.recountDiffs)],
    ['Invoice lines against the requests kept whole in the file', r.lines, ok(r.rawDiffs)],
    ['Amounts due, list prices and model costs, to the billionth of a dollar', r.invoices.length, ok(r.amountDiffs)],
    ['Hourly windows (requests and tokens per customer, model and gateway): late reports in their own hour', r.hourRows, ok(r.hourDiffs)],
    ['Refused reports, by reason, customer, model and gateway', r.refusedRows, ok(r.refusedDiffs)],
    ['September requests in the file: no request id twice', r.rawRows, ok(r.rawRows - r.rawDistinct, 'all distinct')],
    ['Requests served in September', r.servedSept, h('span', { class: nb.never || r.billed !== r.meterSept ? 'bad' : 'ok',
      text: `${fmt.int(r.billed)} billed once${notBilled ? `; ${lostText}` : ''}` })],
  ];
  $('checks').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'What' }), h('th', { class: 'num', text: 'Checked' }), h('th', { text: 'Result' }))),
    h('tbody', {}, rows.map(([what, n, res]) => h('tr', {}, h('td', { text: what }), h('td', { class: 'num', text: fmt.int(n) }), h('td', {}, res)))));
  $('checks-note').textContent = `The invoices read from the precomputes in ${fmt.time(r.invoiceMs)}. Computed from every request in the file, the same amounts take ${fmt.time(r.rawInvoiceMs)}. A quota check, one read of monthly_tokens, takes ${fmt.time(r.quotaUs / 1000)}. `
    + (r.stuck.n ? `The ${fmt.int(notBilled)} requests not billed are the stuck us queue: they were served, but their reports reached the meter after September closed. usage_refused says so, to the request.` : '');
  const saved = r.fileBytes - r.laterBytes;
  $('dispute').replaceChildren(
    h('div', { class: 'bigpair' },
      h('div', {}, h('div', { class: 'n', text: fmt.bytes(r.fileBytes) }), h('div', { class: 'l', text: 'the file at the end of the run, 2 October' })),
      h('div', { class: 'arrow', 'aria-hidden': 'true', text: '→' }),
      h('div', {}, h('div', { class: 'n good', text: fmt.bytes(r.laterBytes) }), h('div', { class: 'l', text: 'the same file on 31 December, distilled' }))),
    h('p', {}, `Distill removed September's ${fmt.int(r.rawRows)} requests and the request ids, ${fmt.bytes(saved)} in all. `,
      h('strong', { class: r.laterSame ? 'ok' : 'bad', text: r.laterSame ? 'The September invoices read the same, to the billionth of a dollar. ' : 'The invoices changed. ' }),
      `The hourly and daily windows stay, and October's ${fmt.int(r.laterOct)} requests stay whole until October's own dispute window ends.`));
  const later = new Map(r.tablesLater.map((t) => [t.name, t]));
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table or index' }), h('th', { class: 'num', text: 'Rows' }), h('th', { class: 'num', text: 'Size' }), h('th', { class: 'num', text: 'On 31 Dec' }))),
    h('tbody', {}, r.tablesNow.filter((t) => t.bytes > 8192 || /^usage_|^_pc_/.test(t.name)).map((t) => h('tr', {},
      h('td', {}, h('code', { text: t.name })),
      h('td', { class: 'num', text: t.rows == null ? '' : fmt.int(t.rows) }),
      h('td', { class: 'num', text: fmt.bytes(t.bytes) }),
      h('td', { class: 'num', text: later.has(t.name) ? fmt.bytes(later.get(t.name).bytes) : '' })))));
  $('file-note').textContent = 'usage_raw holds every request while its month can be disputed; usage_ids the request ids of the last seven days, to refuse repeats. The billing tables and views take a page each.';
  $('download-row').hidden = host === 'artifact';
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      $('sqlite-version').textContent = m.sqlite;
      showCode('policy');
      $('compiler-src').value = m.policy;
      buildPresets();
      setState('idle');
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'usage.sqlite' });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      break;
    }
    case 'error':
      $('hint').replaceChildren(h('span', { class: 'bad', text: m.message }));
      if (state === 'loading') $('clock').textContent = 'Could not start';
      else setState(state === 'finishing' ? 'paused' : state);
      break;
  }
};
worker.onerror = (e) => {
  $('hint').replaceChildren(h('span', { class: 'bad', text: `The demo could not start in this browser (${e.message || 'worker error'}). It needs a current Chrome, Edge, Firefox or Safari.` }));
  $('clock').textContent = 'Could not start';
};

// The policy and its SQL
function showCode(which) {
  for (const id of ['policy', 'sql', 'billing', 'compiler']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  $('code').hidden = which === 'compiler';
  $('compiler').hidden = which !== 'compiler';
  if (!files) return;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'sql') highlight($('code'), files.schema + '\n' + files.distill, 'sql');
  if (which === 'billing') highlight($('code'), files.billing, 'sql');
}
for (const id of ['policy', 'sql', 'billing', 'compiler']) $(`tab-${id}`).addEventListener('click', () => showCode(id));
$('compiler-run').addEventListener('click', async () => {
  $('compiler-status').textContent = 'Loading the compiler…';
  try {
    const compile = await loadCompiler(new URL('../../lib/go/', import.meta.url).href);
    const t0 = performance.now();
    const res = compile($('compiler-src').value, 'policy.precompute');
    const ms = performance.now() - t0;
    const out = $('compiler-out');
    out.hidden = false;
    if (res.error) {
      out.replaceChildren(h('span', { class: 'bad', text: `Line ${res.error}` }));
      $('compiler-status').textContent = 'The policy has a problem.';
    } else {
      highlight(out, res.schema, 'sql');
      $('compiler-status').textContent = `Compiled in ${fmt.time(ms)}: ${res.schema.split('\n').length} lines of SQL.`;
    }
  } catch (e) {
    $('compiler-status').textContent = String(e.message || e);
  }
});

// Ask the meter
function buildPresets() {
  const presets = [
    ['September invoices', "SELECT name, plan, requests, input_tokens, output_tokens,\n  printf('%.2f', due_cents / 100.0) AS due_usd,\n  printf('%.2f', cost_nano / 1e9) AS model_cost_usd\nFROM invoices WHERE period = '2026-09'\nORDER BY due_nano DESC"],
    ['One customer, line by line', "SELECT period, model, requests, input_tokens, output_tokens,\n  list_nano AS list_price_nano_usd\nFROM invoice_lines WHERE customer = 'harbor'\nORDER BY period, model"],
    ['Quotas', 'SELECT customer, period, CAST(used AS INTEGER) AS used, CAST(lim AS INTEGER) AS quota,\n  CAST(remaining AS INTEGER) AS remaining, reached\nFROM monthly_tokens ORDER BY period, customer'],
    ['Refused, and why', 'SELECT reason, customer, model, gateway, n AS reports\nFROM usage_refused ORDER BY reason, n DESC'],
    ['Reconcile with every request', `SELECT l.customer, l.model, l.requests, r.requests AS raw_requests,\n  l.input_tokens - r.input_tokens AS input_diff, l.output_tokens - r.output_tokens AS output_diff\nFROM invoice_lines l JOIN (\n  SELECT customer, model, count(*) AS requests, sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens\n  FROM usage_raw WHERE ts >= ${START} AND ts < ${MONTH_END} GROUP BY customer, model) r USING (customer, model)\nWHERE l.period = '2026-09' ORDER BY l.customer, l.model`],
    ['Margin by customer', "SELECT name, printf('%.2f', due_nano / 1e9) AS revenue_usd, printf('%.2f', cost_nano / 1e9) AS model_cost_usd,\n  printf('%.1f%%', 100.0 * (due_nano - cost_nano) / nullif(due_nano, 0)) AS margin\nFROM invoices WHERE period = '2026-09' ORDER BY due_nano DESC"],
    ['Busiest hours', "SELECT datetime(w, 'unixepoch') AS hour_utc, sum(n) AS requests, CAST(sum(tokens_sum) AS INTEGER) AS tokens\nFROM usage_win WHERE res = 3600 GROUP BY w ORDER BY requests DESC LIMIT 10"],
    ['Find a request', "SELECT datetime(ts, 'unixepoch') AS time_utc, request_id, customer, model, gateway, input_tokens, output_tokens\nFROM usage_raw WHERE request_id = 'r0000ff'"],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
    ['What the file says about itself', 'SELECT name, kind, detail FROM _precomputing_objects'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[0][1];
}
function runSql() {
  if ($('sql-run').disabled) { $('sql-status').textContent = 'Pause the month to ask the meter.'; return; }
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

setState('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
