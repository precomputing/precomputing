// Demo 4 page: the raw lines, the templates, the dashboard drawn from what was sent, alerts, cost,
// search and the SQL box. The reducer and the Engine run in worker.js.
import { $, h, s, fmt, renderResult, highlight, isEmbedded } from '../../lib/kit.js';
import { START, DURATION, INCIDENT, DEPLOY, DEFAULT_RATE } from './scenario.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });
const MONTH = 30 * 86400;

let state = 'loading';
let files = null;
let plans = [];
let panels = [];
let last = null;
let started = false;
let queryId = 0, searchId = 0, importId = 0;
const logged = new Set();

const hms = (t) => { const x = (START + t) % 86400; return `${String(Math.floor(x / 3600)).padStart(2, '0')}:${String(Math.floor(x / 60) % 60).padStart(2, '0')}:${String(x % 60).padStart(2, '0')}`; };
const hm = (t) => hms(t).slice(0, 5);
const compact = (v) => (v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e4 ? `${Math.round(v / 1e3)}k` : v >= 1000 ? `${(v / 1e3).toFixed(1)}k` : v >= 100 ? String(Math.round(v)) : v >= 10 ? v.toFixed(1).replace(/\.0$/, '') : String(Number(v.toPrecision(2))));
const money = (v) => (v >= 100 ? `$${Math.round(v).toLocaleString('en-US')}` : `$${v.toFixed(2)}`);
const COLORS = ['#0E7C86', '#2A4B75', '#B8700F', '#C0392B', '#2E7D4F', '#7B5EA7', '#3A86C8', '#8A95A3', '#A0522D'];

// PanelChart draws a few series minute by minute across the two hours, with a hover readout.
class PanelChart {
  constructor(el, fmtValue) {
    this.el = el;
    this.fmtValue = fmtValue;
    this.points = [];
    this.keys = [];
    this.hover = -1;
    this.tip = h('div', { class: 'tip', hidden: true });
    el.append(this.tip);
    el.tabIndex = 0;
    el.addEventListener('pointermove', (e) => this.onPointer(e));
    el.addEventListener('pointerleave', () => { this.hover = -1; this.render(); });
    el.addEventListener('keydown', (e) => {
      if (!this.points.length) return;
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault();
        this.hover = this.hover < 0 ? this.points.length - 1 : Math.max(0, Math.min(this.points.length - 1, this.hover + (e.key === 'ArrowRight' ? 1 : -1)));
        this.render();
      }
    });
    el.addEventListener('blur', () => { this.hover = -1; this.render(); });
    new ResizeObserver(() => this.render()).observe(el);
  }

  setData(points, keys) { this.points = points; this.keys = keys; this.render(); }

  geom() {
    const w = Math.max(240, this.el.clientWidth);
    return { w, h: 150, x0: 40, x1: w - 8, y0: 8, y1: 124 };
  }

  onPointer(e) {
    if (!this.points.length) return;
    const g = this.geom();
    const x = e.clientX - this.el.getBoundingClientRect().left;
    const t = ((x - g.x0) / (g.x1 - g.x0)) * DURATION;
    let best = 0;
    for (let i = 1; i < this.points.length; i++) if (Math.abs(this.points[i].t - t) < Math.abs(this.points[best].t - t)) best = i;
    this.hover = best;
    this.render();
  }

  render() {
    const g = this.geom();
    const pts = this.points;
    let max = 0;
    for (const p of pts) for (const k of this.keys) max = Math.max(max, p.v.get(k) || 0);
    const niceMax = (() => { if (!(max > 0)) return 1; const e = Math.pow(10, Math.floor(Math.log10(max))); for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (max * 1.05 <= m * e) return m * e; return 10 * e; })();
    const X = (t) => g.x0 + ((t + 30) / DURATION) * (g.x1 - g.x0);
    const Y = (v) => g.y1 - (v / niceMax) * (g.y1 - g.y0);
    const svg = s('svg', { viewBox: `0 0 ${g.w} ${g.h}`, height: g.h, role: 'img', 'aria-label': this.el.getAttribute('aria-label') || 'Panel chart' });
    for (const v of [0, niceMax / 2, niceMax]) {
      svg.append(s('line', { x1: g.x0, x2: g.x1, y1: Y(v), y2: Y(v), stroke: v === 0 ? 'var(--gray-2)' : 'var(--grid)', 'stroke-width': 1 }));
      svg.append(s('text', { x: g.x0 - 6, y: Y(v) + 4, 'text-anchor': 'end' }, compact(v)));
    }
    for (const t of [0, 1800, 3600, 5400, 7200]) svg.append(s('text', { x: g.x0 + (t / DURATION) * (g.x1 - g.x0), y: g.h - 6, 'text-anchor': t === 0 ? 'start' : t === DURATION ? 'end' : 'middle' }, hm(t)));
    const marks = [[INCIDENT.from, INCIDENT.to, 'var(--red)'], ...(last?.shards || []).map((x) => [x.from, x.to, 'var(--red)'])];
    for (const [a, b, c] of marks) if (pts.length && pts[pts.length - 1].t + 60 > a) {
      svg.prepend(s('rect', { x: X(a - 30), y: g.y0, width: Math.max(2, X(Math.min(b, pts[pts.length - 1].t + 60) - 30) - X(a - 30)), height: g.y1 - g.y0, fill: c, 'fill-opacity': 0.1 }));
    }
    this.keys.forEach((k, i) => {
      let d = '';
      pts.forEach((p, j) => { const v = p.v.get(k) || 0; d += `${j ? 'L' : 'M'}${X(p.t).toFixed(1)} ${Y(v).toFixed(1)}`; });
      if (d) svg.append(s('path', { d, fill: 'none', stroke: COLORS[i % COLORS.length], 'stroke-width': 1.6, 'stroke-linejoin': 'round' }));
    });
    if (this.hover >= 0 && pts[this.hover]) {
      const p = pts[this.hover];
      svg.append(s('line', { x1: X(p.t), x2: X(p.t), y1: g.y0, y2: g.y1, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
      const rows = this.keys.map((k, i) => ({ k, i, v: p.v.get(k) })).filter((r) => r.v != null).sort((a, b) => b.v - a.v).slice(0, 9);
      this.tip.replaceChildren(h('div', { class: 't', text: `${hm(p.t)} to ${hm(p.t + 60)}` }),
        ...rows.map((r) => h('div', { class: 'r' }, h('i', { style: `border-color:${COLORS[r.i % COLORS.length]}` }), h('b', { text: this.fmtValue(r.v) }), h('span', { class: 'nm', text: r.k || 'all' }))));
      this.tip.hidden = false;
      const tx = X(p.t) + 12;
      this.tip.style.left = (tx + 190 > g.w ? Math.max(0, X(p.t) - 200) : tx) + 'px';
      this.tip.style.top = '0px';
    } else this.tip.hidden = true;
    this.el.querySelector('svg')?.remove();
    this.el.prepend(svg);
  }
}

function describe(p) {
  const what = p.measure === 'count' ? 'lines' : p.measure === 'rate' ? 'lines a second' : `${p.measure} of ${p.field}`;
  const where = Object.entries(p.where).map(([k, v]) => `${k} = ${v}`).join(', ');
  return `${what}${p.by.length ? ` by ${p.by.join(' and ')}` : ''}${where ? `, where ${where}` : ''} · stream ${p.stream}`;
}

function buildPanels() {
  panels = plans.map((p) => {
    const chart = h('div', { class: 'chart', 'aria-label': `${p.title}, minute by minute` });
    const bars = h('div', { class: 'bars' });
    const legend = h('div', { class: 'legend mini' });
    const badge = h('div', { class: 'badge' });
    const card = h('section', { class: 'card panel' },
      h('h3', { text: p.title }), h('p', { class: 'sub', text: describe(p) }),
      p.type === 'toplist' ? bars : chart, p.type === 'toplist' ? null : legend, badge);
    $('panels').append(card);
    const unit = p.measure === 'sum' && p.field === 'total' ? (v) => `$${Math.round(v).toLocaleString('en-US')}` : p.field === 'ms' ? (v) => `${v.toFixed(1)} ms` : (v) => fmt.int(v);
    return { p, card, chart: p.type === 'toplist' ? null : new PanelChart(chart, unit), bars, legend, badge, minutes: new Map(), keys: new Map(), unit };
  });
}

function drawPanels() {
  for (const pn of panels) {
    const { p } = pn;
    if (p.type === 'toplist') {
      const tot = new Map();
      for (const mm of pn.minutes.values()) for (const [k, v] of mm) tot.set(k, (tot.get(k) || 0) + v);
      const top = [...tot].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, p.top);
      const max = top.length ? top[0][1] : 1;
      pn.bars.replaceChildren(...(top.length ? top.map(([k, v]) => h('div', { class: 'barrow' },
        h('span', { class: 'k', text: k }), h('span', { class: 'b' }, h('i', { style: `width:${(100 * v) / max}%` })), h('span', { class: 'v', text: fmt.int(v) })))
        : [h('p', { class: 'muted', text: 'Nothing yet.' })]));
    } else {
      // Series in order of their total, the biggest first.
      const keys = [...pn.keys].sort((a, b) => b[1] - a[1]).map(([k]) => k).slice(0, COLORS.length);
      const pts = [...pn.minutes].sort((a, b) => a[0] - b[0]).map(([w, mm]) => ({ t: w - START, v: mm }));
      pn.chart.setData(pts, keys);
      pn.legend.replaceChildren(...keys.map((k, i) => h('span', {}, h('i', { style: `border-color:${COLORS[i]}` }), k || (p.measure === 'sum' ? `${p.measure} of ${p.field}` : 'all'))));
    }
    const c = last?.checks?.[p.i];
    if (c && c.points) {
      const ok = c.same === c.points;
      const pct = p.measure.startsWith('p');
      pn.badge.className = `badge ${ok ? 'ok' : 'bad'}`;
      pn.badge.textContent = ok ? `✓ ${fmt.int(c.points)} points ${pct ? 'within 1% of' : 'the same as'} the raw lines` : `${fmt.int(c.points - c.same)} of ${fmt.int(c.points)} points differ from the raw lines`;
    } else {
      pn.badge.className = 'badge';
      pn.badge.textContent = 'Checked against the raw lines each minute.';
    }
  }
}

function setState(st) {
  state = st;
  const busy = st === 'loading' || st === 'finishing';
  const live = st === 'playing' || st === 'paused';
  $('btn-play').disabled = busy;
  $('play-label').textContent = st === 'playing' ? 'Pause' : st === 'paused' ? 'Resume' : st === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', st === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || st === 'idle';
  $('btn-review').disabled = busy || st === 'done';
  $('btn-shard').disabled = !live;
  $('search-run').disabled = st === 'loading';
  $('sql-run').disabled = !(st === 'paused' || st === 'done' || st === 'idle');
  $('sql-status').textContent = st === 'playing' ? 'Pause the run to ask the file.' : '';
  const hints = {
    idle: 'Press Play. Two hours pass in about a minute, slowing down when something happens. Change the rate or break a search shard as it runs.',
    playing: 'Watch the gap between raw and sent. Break a search shard to see a new error caught, or search the lines kept on site.',
    paused: 'Paused. You can ask the file now, or resume.',
    finishing: 'Finishing the two hours at full speed, then checking every panel.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = hints[st] || '';
}

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) { started = true; log(0, `12:00: the shop's five services start writing, about ${$('rate').value} lines a second.`, 'teal'); }
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) { started = true; log(0, 'The shop starts writing. Running the two hours at full speed.', 'teal'); }
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('rate').addEventListener('input', () => {
  $('rate-val').textContent = $('rate').value;
  worker.postMessage({ type: 'rate', rate: Number($('rate').value) });
});
$('btn-shard').addEventListener('click', () => worker.postMessage({ type: 'shard' }));

function resetView() {
  logged.clear();
  started = false;
  last = null;
  for (const pn of panels) { pn.minutes.clear(); pn.keys.clear(); }
  drawPanels();
  $('log').replaceChildren();
  $('alerts').replaceChildren(h('li', { class: 'muted', text: 'None yet.' }));
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  $('c-rate').textContent = '–';
}

function log(t, text, kind = '') {
  const li = h('li', { 'data-t': t }, h('span', { class: 'when', text: hms(t) }), h('span', { class: `dot ${kind}` }), h('span', { text }));
  const after = [...$('log').children].find((x) => Number(x.dataset.t) <= t);
  $('log').insertBefore(li, after || null);
}

const script = [
  { t: INCIDENT.from, text: `12:40: the payment provider ${INCIDENT.provider} starts failing. Payments and checkout begin to log errors of a kind not seen before.`, kind: 'red' },
  { t: INCIDENT.to, text: `12:48: ${INCIDENT.provider} recovers.`, kind: '' },
  { t: DEPLOY.at, text: '13:05: a deploy of search goes out. It logs three debug lines with every query, and the raw logs grow by a third.', kind: 'amber' },
];

const levelClass = (lv) => ({ ERROR: 'lv-error', WARN: 'lv-warn', DEBUG: 'lv-debug' })[lv] || 'lv-info';

function lineEl(line) {
  const m = /^(\S+)\s+(\S+)\s+(\S+)\s+(.*)$/.exec(line);
  if (!m) return h('div', { class: 'ln', text: line });
  return h('div', { class: 'ln' }, h('span', { class: 'ts', text: m[1].slice(11, 23) }), ' ', h('span', { class: `lv ${levelClass(m[2])}`, text: m[2].padEnd(5) }), ' ',
    h('span', { class: 'svc', text: m[3] }), ' ', m[4]);
}

function templateEl(text) {
  const parts = text.split('<*>');
  const out = [];
  parts.forEach((x, i) => { if (i) out.push(h('span', { class: 'wild', text: '<*>' })); if (x) out.push(x); });
  return h('code', {}, ...out);
}

function costRows(m) {
  const secs = Math.max(1, m.t);
  const scale = MONTH / secs;
  const pGb = Number($('p-gb').value) || 0, pEv = Number($('p-ev').value) || 0;
  const row = (label, bytes, events) => {
    const gb = (bytes * scale) / 1e9, ev = events * scale;
    return { label, gb, ev, cost: gb * pGb + (ev / 1e6) * pEv };
  };
  return [row('Every line upstream', m.rawBytes, m.lines), row('What was sent', m.sentBytes, m.sentEvents)];
}

function drawCost() {
  if (!last || !last.t) {
    $('cost').tBodies[0].replaceChildren(h('tr', {}, h('td', { colspan: 4, class: 'muted', text: 'Press Play to see the month.' })));
    return;
  }
  const rows = costRows(last);
  $('cost').tBodies[0].replaceChildren(...rows.map((r, i) => h('tr', { class: i ? 'good-row' : null },
    h('td', { text: r.label }), h('td', { class: 'num', text: r.gb.toFixed(r.gb < 1 ? 2 : 1) }), h('td', { class: 'num', text: compact(r.ev) }), h('td', { class: 'num strong', text: money(r.cost) }))));
}
$('p-gb').addEventListener('input', drawCost);
$('p-ev').addEventListener('input', drawCost);

function onSnapshot(m) {
  last = m;
  const t = m.t;
  $('bar').style.width = `${(100 * t) / m.duration}%`;
  $('clock').textContent = `${hms(t)} UTC${m.slow && state === 'playing' ? ' · slow motion' : ''}`;
  $('c-lines').textContent = fmt.int(m.lines);
  $('c-raw').textContent = fmt.bytes(m.rawBytes);
  $('c-sent').textContent = fmt.bytes(m.sentBytes);
  $('c-ratio').textContent = m.sentBytes ? fmt.times(m.rawBytes / m.sentBytes) : '–';
  $('c-templates').textContent = fmt.int(m.templates.length);
  if (m.engineMs > 50) $('c-rate').textContent = fmt.int(m.lines / (m.engineMs / 1000));
  $('tail').replaceChildren(...m.tail.map(lineEl));
  const tpl = [...m.templates].sort((a, b) => b.firstTs - a.firstTs || b.id - a.id);
  const newest = tpl.length ? tpl[0].firstTs : 0;
  $('templates').tBodies[0].replaceChildren(...tpl.map((x) => h('tr', { class: x.firstTs - START >= 300 && x.firstTs >= newest - 60 && t - (x.firstTs - START) < 300 ? 'fresh' : null, title: x.example },
    h('td', { class: 'num', text: String(x.id) }), h('td', { text: x.service }), h('td', {}, h('span', { class: `lv ${levelClass(x.level)}`, text: x.level })),
    h('td', { class: 'num', text: fmt.int(x.n) }), h('td', { class: 'tpl' }, templateEl(x.text)))));
  for (const mi of m.minutes) {
    for (const [i, rows] of mi.panels.entries()) {
      const pn = panels[i];
      if (!pn) continue;
      const mm = new Map();
      for (const r of rows) {
        const k = r.slice(0, pn.p.by.length).join(' · ');
        const v = r[pn.p.by.length];
        mm.set(k, v);
        pn.keys.set(k, (pn.keys.get(k) || 0) + v);
      }
      pn.minutes.set(mi.w, mm);
    }
  }
  if (m.minutes.length || !panels.some((pn) => pn.minutes.size)) drawPanels();
  else for (const pn of panels) pn.badge && drawBadgeOnly(pn);
  if (m.alerts.length) {
    $('alerts').replaceChildren(...m.alerts.slice().reverse().map((a) => h('li', {},
      h('span', { class: 'when', text: hms(a.first) }), h('span', { class: `dot ${a.level === 'ERROR' ? 'red' : 'amber'}` }),
      h('span', {}, h('strong', { text: `${a.level} in ${a.service}, caught in ${a.caughtAfter} s. ` }), templateEl(a.text),
        h('div', { class: 'ex' }, lineEl(a.example))))));
  }
  for (const n of m.news) log(n.t, n.text, n.kind);
  if (started) for (const sc of script) if (t > sc.t && !logged.has(sc.t)) { logged.add(sc.t); log(sc.t, sc.text, sc.kind); }
  if (m.done && !logged.has('done')) { logged.add('done'); log(m.duration, '14:00: two hours done. The results are below.', 'teal'); }
  drawCost();
}

function drawBadgeOnly(pn) {
  const c = last?.checks?.[pn.p.i];
  if (!c || !c.points) return;
  const ok = c.same === c.points;
  const pct = pn.p.measure.startsWith('p');
  pn.badge.className = `badge ${ok ? 'ok' : 'bad'}`;
  pn.badge.textContent = ok ? `✓ ${fmt.int(c.points)} points ${pct ? 'within 1% of' : 'the same as'} the raw lines` : `${fmt.int(c.points - c.same)} of ${fmt.int(c.points)} points differ from the raw lines`;
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const perSec = r.lines / (r.engineMs / 1000);
  $('c-rate').textContent = fmt.int(perSec);
  const pay = r.alerts.find((a) => a.service === 'payments');
  const allOk = r.checks.every((c) => c.same === c.points);
  const cost = costRows({ t: DURATION, rawBytes: r.rawBytes, lines: r.lines, sentBytes: r.sentBytes, sentEvents: r.sentEvents });
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.lines)} lines in two hours, ${fmt.bytes(r.rawBytes)}. ` }),
    `Upstream got ${fmt.bytes(r.sentBytes)} in ${fmt.int(r.batches)} batches: ${fmt.times(r.rawBytes / r.sentBytes)} fewer bytes and ${fmt.times(r.lines / r.sentEvents)} fewer events. `,
    allOk ? 'Every panel of the dashboard drew the same values from them as a recount of the raw lines, percentiles within 1%. ' : 'Some panel values differ from the raw lines. ',
    pay ? `The payment incident was caught ${pay.caughtAfter} seconds after its first error line, as a new template. ` : '',
    `At Datadog's list prices a month like this costs about ${money(cost[0].cost)} with every line upstream and ${money(cost[1].cost)} with what was sent. `,
    `All ${fmt.int(r.rawRows)} lines stay in the file on site, searchable for 48 hours; this device read them at about ${fmt.int(perSec)} lines a second.`);
  const rows = plans.map((p) => {
    const c = r.checks[p.i];
    const pct = p.measure.startsWith('p');
    return [p.title, c.points, c.same === c.points ? (pct ? `within 1% (worst ${(c.worst * 100).toFixed(2)}%)` : 'identical') : `${c.points - c.same} differ`, c.same === c.points];
  });
  for (const tp of r.tops) {
    const same = JSON.stringify(tp.sent) === JSON.stringify(tp.raw);
    rows.push([`${plans[tp.i].title}: the top ${tp.sent.length} over two hours`, tp.sent.length, same ? 'identical' : 'differ', same]);
  }
  $('checks').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Panel' }), h('th', { class: 'num', text: 'Points' }), h('th', { text: 'Against the raw lines' }))),
    h('tbody', {}, rows.map(([what, n, res, ok]) => h('tr', {}, h('td', { text: what }), h('td', { class: 'num', text: fmt.int(n) }), h('td', {}, h('span', { class: ok ? 'ok' : 'bad', text: res }))))));
  $('checks-note').textContent = `A point is one value of one series in one minute. Percentiles come from sketches that promise to be within 1% of the exact value. `
    + `The templates come from Drain, ported from its reference implementation: on each of the 16 Loghub samples its grouping accuracy equals the published figure. `
    + `Searching all ${fmt.int(r.rawRows)} lines kept on site for “${r.search.text}” found ${fmt.int(r.search.n)} in ${fmt.time(r.search.ms)}.`;
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table or index' }), h('th', { class: 'num', text: 'Rows' }), h('th', { class: 'num', text: 'Size' }))),
    h('tbody', {}, r.tables.filter((x) => x.rows !== 0 || x.bytes > 8192).map((x) => h('tr', {}, h('td', {}, h('code', { text: x.name })), h('td', { class: 'num', text: x.rows == null ? '' : fmt.int(x.rows) }), h('td', { class: 'num', text: fmt.bytes(x.bytes) })))));
  $('file-note').textContent = 'lines_raw holds every line for 48 hours with its template; the panel streams hold 1-minute windows for 30 days and keep no raw lines. Empty tables are left out.';
  $('download-row').hidden = host === 'artifact';
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      plans = m.plans;
      $('sqlite-version').textContent = m.sqlite;
      $('pc-version').textContent = m.version;
      buildPanels();
      drawPanels();
      showCode('dashboard');
      $('import-src').value = m.dashboard;
      buildPresets();
      setState('idle');
      $('alerts').replaceChildren(h('li', { class: 'muted', text: 'None yet.' }));
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'search':
      if (m.id === searchId) {
        const r = m.result;
        $('search-status').textContent = `${fmt.int(r.n)} lines in ${fmt.time(r.ms)}${r.n > r.rows.length ? `, the newest ${r.rows.length} shown` : ''}`;
        $('search-out').replaceChildren(...(r.rows.length ? r.rows.map(([, line]) => lineEl(line)) : [h('p', { class: 'muted', text: 'No lines match.' })]));
      }
      break;
    case 'import':
      if (m.id === importId) showImport(m.result);
      break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'shop-logs.sqlite' });
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

// Search
$('search-form').addEventListener('submit', (e) => {
  e.preventDefault();
  const text = $('search-text').value.trim();
  if (!text) return;
  searchId++;
  $('search-status').textContent = 'Searching…';
  worker.postMessage({ type: 'search', id: searchId, text });
});

// The dashboard, its policy and the import
function showCode(which) {
  for (const id of ['dashboard', 'policy', 'sql', 'import']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  $('code').hidden = which === 'import';
  $('importer').hidden = which !== 'import';
  if (!files) return;
  if (which === 'dashboard') $('code').textContent = files.dashboard;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'sql') highlight($('code'), files.schema + '\n' + files.distill, 'sql');
}
for (const id of ['dashboard', 'policy', 'sql', 'import']) $(`tab-${id}`).addEventListener('click', () => showCode(id));
$('import-run').addEventListener('click', () => {
  importId++;
  $('import-status').textContent = 'Importing…';
  worker.postMessage({ type: 'import', id: importId, text: $('import-src').value });
});
function showImport(res) {
  const out = $('import-out');
  out.hidden = false;
  if (res.error) {
    out.replaceChildren(h('span', { class: 'bad', text: res.error }));
    $('import-status').textContent = 'The dashboard has a problem.';
    return;
  }
  highlight(out, res.policy, 'policy');
  $('import-status').textContent = `${res.plans.length} panels, kept ready by ${new Set(res.plans.map((p) => p.stream)).size} streams.`;
}

// Ask the file
function buildPresets() {
  const time = (c) => `time(${c}, 'unixepoch')`;
  const presets = [
    ['Templates, most lines first', 'SELECT id, service, level, n AS lines, template\nFROM _precomputing_templates ORDER BY n DESC'],
    ['The incident, line by line', `SELECT ${time('ts')} AS utc, line FROM lines_raw\nWHERE line LIKE '%ERROR%' AND line LIKE '%${INCIDENT.provider}%'\nORDER BY ts LIMIT 50`],
    ['Requests a minute by route', `SELECT ${time('w')} AS minute, route, n AS requests\nFROM web_by_route_win WHERE res = 60\nORDER BY w DESC, n DESC LIMIT 35`],
    ['Error lines kept whole', `SELECT ${time('ts')} AS utc, service, template, line\nFROM errors_sample ORDER BY ts DESC LIMIT 30`],
    ['Slow requests kept whole', `SELECT ${time('ts')} AS utc, route, ms, round(z, 1) AS z, line\nFROM web_ms_by_route_anomaly ORDER BY ts`],
    ['Lines per template, per 10 minutes', `SELECT ${time('w')} AS from_utc, t.service, t.level, sum(l.n) AS lines, t.template\nFROM lines_win l JOIN _precomputing_templates t ON t.id = l.template\nWHERE l.res = 600 GROUP BY l.w, l.template ORDER BY l.w DESC, lines DESC LIMIT 40`],
    ['Top searches', 'SELECT q, sum(n) AS searches FROM search_by_q_win WHERE res = 60\nGROUP BY q ORDER BY searches DESC LIMIT 10'],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
    ['What the file says about itself', 'SELECT name, kind, stream, detail FROM _precomputing_objects'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[0][1];
}
function runSql() {
  if ($('sql-run').disabled) { $('sql-status').textContent = 'Pause the run to ask the file.'; return; }
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

$('rate').value = String(DEFAULT_RATE);
$('rate-val').textContent = String(DEFAULT_RATE);
setState('loading');
drawCost();
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
