// Demo 1 page: controls, live figures, charts and the SQL box. The engine runs in worker.js.
import { $, h, fmt, timeOfDay, LineChart, Multiples, renderResult, highlight, loadCompiler, isEmbedded } from '../../lib/kit.js';
import { START, DURATION, ENDPOINTS, DEFAULT_RATE } from './scenario.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });

let state = 'loading';      // loading, idle, playing, paused, finishing, done
let files = null;
let gamma = null;
let series = [];
let last = null;             // the latest snapshot
let rateSample = null;       // for requests a second on this device
let started = false;
let queryId = 0;
const logged = new Set();
const bands = [];

const hourTicks = [0, 3600, 7200, 10800];
const label = (t) => timeOfDay(START, t);

const sizeChart = new LineChart($('size-chart'), {
  height: 230, xMax: DURATION, xTicks: hourTicks, xLabel: label,
  yFormat: (v, precise) => (precise ? fmt.bytes(v) : v === 0 ? '0' : (v / 1e6).toFixed(v < 1e7 && v % 1e6 ? 1 : 0) + ' MB'),
  series: [
    { key: 'raw', name: 'Raw table', color: 'var(--gray-2)' },
    { key: 'pc', name: 'Precomputed file', color: 'var(--teal)' },
  ],
  aria: 'Line chart of storage over the simulated three hours: the raw table and the precomputed file.',
});
const p99Chart = new Multiples($('p99-chart'), {
  rows: ENDPOINTS, xMax: DURATION, xTicks: hourTicks, xLabel: label, color: 'var(--teal)', key: 'p99',
  aria: 'Five small line charts of p99 latency per minute, one per endpoint.',
});

// Controls
for (const [i, e] of ENDPOINTS.entries()) $('outage-ep').append(h('option', { value: i, text: e }));
$('outage-ep').value = '1';

function setState(s) {
  state = s;
  const busy = s === 'loading' || s === 'finishing';
  $('btn-play').disabled = busy;
  $('play-label').textContent = s === 'playing' ? 'Pause' : s === 'paused' ? 'Resume' : s === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', s === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || s === 'idle';
  $('btn-review').disabled = busy || s === 'done';
  $('btn-spike').disabled = !(s === 'playing' || s === 'paused');
  $('btn-outage').disabled = !(s === 'playing' || s === 'paused');
  $('sql-run').disabled = !(s === 'paused' || s === 'done' || s === 'idle');
  $('sql-status').textContent = s === 'playing' ? 'Pause the run to ask the file.' : '';
  const hints = {
    idle: 'Press Play. Three simulated hours pass in about a minute on a recent computer. Slow the traffic down or speed it up as it runs.',
    playing: 'Add a slow request or start an outage while it runs, then watch the charts and the log.',
    paused: 'Paused. You can ask the file now, or resume.',
    finishing: 'Finishing the run at full speed.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = hints[s] || '';
}

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) { started = true; log(0, `Traffic starts: about ${Number($('traffic').value)} requests a second across five endpoints.`, 'teal'); }
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) { started = true; log(0, 'Traffic starts. Running the three hours at full speed.', 'teal'); }
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('traffic').addEventListener('input', () => {
  const r = Number($('traffic').value);
  $('traffic-val').textContent = `${r} requests a second`;
  worker.postMessage({ type: 'traffic', rate: r });
});
$('btn-spike').addEventListener('click', () => worker.postMessage({ type: 'spike' }));
$('btn-outage').addEventListener('click', () => {
  const i = Number($('outage-ep').value);
  worker.postMessage({ type: 'outage', endpoint: i, name: ENDPOINTS[i] });
});

function resetView() {
  series = [];
  bands.length = 0;
  logged.clear();
  started = false;
  rateSample = null;
  $('log').replaceChildren();
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  sizeChart.setData([]);
  p99Chart.setData([], []);
}

function log(t, text, kind = '') {
  $('log').prepend(h('li', {}, h('span', { class: 'when', text: label(t) }), h('span', { class: `dot ${kind}` }), h('span', { text })));
}

// Scripted moments, logged as the simulated clock passes them.
const script = [
  { t: 300, text: 'Requests older than five minutes start fading into summaries.', kind: '' },
  { t: 3600, text: null, kind: 'teal' },
  { t: 5400, text: 'Outage: /api/checkout runs six times slower for two minutes.', kind: 'red' },
  { t: 5520, text: '/api/checkout recovers.', kind: '' },
  { t: 7200, text: null, kind: 'teal' },
];

function onSnapshot(m) {
  last = m;
  if (m.series.length) {
    if (m.series[0].t === 0) series = [];
    for (const pt of m.series) {
      if (series.length && series[series.length - 1].t === pt.t) series[series.length - 1] = pt;
      else series.push(pt);
    }
  }
  const t = m.t;
  $('bar').style.width = `${(100 * t) / m.duration}%`;
  $('clock').textContent = `${fmt.clock(t)} of ${fmt.clock(m.duration)}`;
  $('c-events').textContent = fmt.int(m.events);
  const now = performance.now();
  if (state === 'playing' || state === 'finishing') {
    if (rateSample && now - rateSample.at > 900) {
      const r = ((m.events - rateSample.events) / (now - rateSample.at)) * 1000;
      if (r > 0) $('c-rate').textContent = fmt.int(r);
      rateSample = { at: now, events: m.events };
    } else if (!rateSample) rateSample = { at: now, events: m.events };
  }
  $('c-raw').textContent = fmt.bytes(m.rawBytes);
  $('c-pc').textContent = fmt.bytes(m.pcBytes);
  $('c-ratio').textContent = m.rawBytes > m.pcBytes ? fmt.times(m.rawBytes / m.pcBytes) : '–';
  $('c-anom').textContent = fmt.int(m.anomalies);
  const tb = $('answers').tBodies[0];
  tb.replaceChildren(...m.answers.rows.map((a) => h('tr', {},
    h('td', {}, h('code', { text: a.endpoint })),
    h('td', { class: 'num', text: fmt.int(a.requests) }),
    h('td', { class: 'num', text: fmt.ms(a.avg) }),
    h('td', { class: 'num', text: fmt.ms(a.p99) }))));
  $('answers-sub').textContent = m.events ? `All 15 answers read from the precompute views in ${fmt.time(m.answers.readMs)}, updated as requests arrive.` : 'Read from the precompute views as requests arrive.';
  for (const s of script) {
    if (t >= s.t && !logged.has(s.t) && started) {
      logged.add(s.t);
      const text = s.text || `${s.t / 3600} hour${s.t > 3600 ? 's' : ''} in: ${fmt.int(m.events)} requests, ${fmt.int(m.anomalies)} unusual ones kept whole.`;
      log(s.t, text, s.kind);
    }
  }
  const shown = [];
  for (const o of m.outages) if (o.from < t) shown.push({ row: o.endpoint, from: o.from, to: Math.min(o.to, t) });
  sizeChart.setData(series);
  p99Chart.setData(series, shown);
  if (m.done && !logged.has('done')) { logged.add('done'); log(m.duration, 'Three hours done. The results are below.', 'teal'); }
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const perSec = r.events / (r.ingestMs / 1000);
  $('c-rate').textContent = fmt.int(perSec);
  $('c-pc').textContent = fmt.bytes(r.pcBytes);
  $('c-ratio').textContent = fmt.times(r.reduction);
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.events)} requests in three hours. ` }),
    `The raw table takes ${fmt.bytes(r.rawBytes)}. The precomputed file takes ${fmt.bytes(r.pcBytes)}, ${fmt.times(r.reduction)} smaller, and it still answers every question below. `,
    `Of that, ${fmt.bytes(r.pcBytes - r.rawTierBytes)} is three hours of summaries, sketches, samples and unusual requests (${fmt.times(r.rawBytes / (r.pcBytes - r.rawTierBytes))} smaller than the raw table), and ${fmt.bytes(r.rawTierBytes)} is the last five minutes kept whole. `,
    `This device took them in at about ${fmt.int(perSec)} a second, writing both the file and the raw table.`);
  const head = h('thead', {}, h('tr', {}, ['Endpoint', 'Requests', 'Average, precomputed', 'Average, exact', 'p99, precomputed', 'p99, exact', 'p99 difference']
    .map((c, i) => h('th', { class: i ? 'num' : null, text: c }))));
  const body = h('tbody', {}, r.rows.map((x) => h('tr', {},
    h('td', {}, h('code', { text: x.endpoint })),
    h('td', { class: 'num' }, fmt.int(x.requests), ' ', h('span', { class: x.requests === x.requestsExact ? 'ok' : 'bad', text: x.requests === x.requestsExact ? '✓' : `≠ ${fmt.int(x.requestsExact)}` })),
    h('td', { class: 'num', text: x.avg.toFixed(2) + ' ms' }),
    h('td', { class: 'num', text: x.avgExact.toFixed(2) + ' ms' }),
    h('td', { class: 'num', text: x.p99.toFixed(1) + ' ms' }),
    h('td', { class: 'num', text: x.p99Exact.toFixed(1) + ' ms' }),
    h('td', { class: 'num' }, h('span', { class: x.p99Error <= 0.01 ? 'ok' : 'bad', text: fmt.pct(x.p99Error) })))));
  $('cmp').replaceChildren(head, body);
  $('cmp-note').textContent = `Counts and averages are exact. Percentiles come from sketches that promise to be within 1% of the exact value. `
    + `Reading all 15 answers took ${fmt.time(r.readAllMs)} from the precompute views and ${fmt.time(r.rawAllMs)} from the raw table, a plain table with no index. `
    + `One count and one average take ${fmt.time(r.simpleReadUs / 1000)}.`;
  const u = $('unusual');
  u.replaceChildren(...[
    h('p', {}, h('strong', { text: `${r.spikes.plannedKept} of ${r.spikes.planned}` }), ' planned slow requests were kept whole, each 8 to 20 times the usual latency of its endpoint.',
      r.spikes.missedInFullMinute ? ` ${r.spikes.missedInFullMinute === 1 ? 'The one not kept' : `The ${r.spikes.missedInFullMinute} not kept`} came during an outage, in a minute whose allowance of 20 was already used; ${r.spikes.missedInFullMinute === 1 ? 'it is' : 'they are'} still counted in that minute's summary.` : ''),
    r.spikes.yours ? h('p', {}, h('strong', { text: `${r.spikes.yoursKept} of ${r.spikes.yours}` }), ' of the slow requests you added were kept whole.') : null,
    ...r.outages.map((o) => h('p', {}, `During the ${o.visitor ? 'outage you started' : 'scripted outage'} on `, h('code', { text: o.endpoint }), ` (${label(o.from)} to ${label(o.to)}), `,
      h('strong', { text: `${fmt.int(o.flagged)} of ${fmt.int(o.requests)}` }), ` requests were counted as unusual and ${fmt.int(o.keptWhole)} were kept whole. The policy keeps at most 20 a minute per endpoint, so a storm cannot fill the file.`)),
    h('p', { class: 'note', text: `In all, ${fmt.int(r.anomalies)} unusual requests are kept whole in latency_anomaly.` })].filter(Boolean));
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table' }), h('th', { class: 'num', text: 'Rows' }))),
    h('tbody', {}, r.tableRows.map(([n, c]) => h('tr', {}, h('td', {}, h('code', { text: n })), h('td', { class: 'num', text: fmt.int(c) })))));
  $('file-note').textContent = `${fmt.bytes(r.rawTierBytes)} of the file is the last five minutes kept whole in latency_raw and its time index, as the policy asks. The rest is three hours of summaries, sketches, samples and unusual requests.`;
  $('download-row').hidden = host === 'artifact';
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      gamma = m.gamma;
      $('sqlite-version').textContent = m.sqlite;
      showCode('policy');
      $('compiler-src').value = m.policy;
      buildPresets();
      setState('idle');
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'event':
      log(m.t, m.text, m.kind === 'outage' ? 'red' : 'amber');
      break;
    case 'query':
      if (m.id === queryId) renderResult($('sql-out'), m.result);
      break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'latency.sqlite' });
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
  for (const id of ['policy', 'sql', 'compiler']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  $('code').hidden = which === 'compiler';
  $('compiler').hidden = which !== 'compiler';
  if (!files) return;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'sql') highlight($('code'), files.schema + '\n' + files.distill, 'sql');
}
$('tab-policy').addEventListener('click', () => showCode('policy'));
$('tab-sql').addEventListener('click', () => showCode('sql'));
$('tab-compiler').addEventListener('click', () => showCode('compiler'));
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

// Ask the file
function buildPresets() {
  const g = gamma;
  const presets = [
    ['Answers kept ready', 'SELECT r.endpoint, r.value AS requests, round(a.value, 2) AS avg_ms, round(p.value, 1) AS p99_ms\nFROM requests r JOIN avg_ms a USING (endpoint) JOIN p99_ms p USING (endpoint)\nORDER BY requests DESC'],
    ['p99 per hour', `WITH c AS (\n  SELECT w, endpoint, b,\n    sum(n) OVER (PARTITION BY w, endpoint ORDER BY b ROWS UNBOUNDED PRECEDING) AS cum,\n    sum(n) OVER (PARTITION BY w, endpoint) AS tot\n  FROM latency_ms_sk WHERE res = 3600)\nSELECT time(w, 'unixepoch') AS hour, endpoint, round(2 * pow(${g}, min(b)) / (${g} + 1), 1) AS p99_ms\nFROM c WHERE cum >= 0.99 * tot GROUP BY w, endpoint ORDER BY w, endpoint`],
    ['Slowest minutes', `WITH c AS (\n  SELECT w, endpoint, b,\n    sum(n) OVER (PARTITION BY w, endpoint ORDER BY b ROWS UNBOUNDED PRECEDING) AS cum,\n    sum(n) OVER (PARTITION BY w, endpoint) AS tot\n  FROM latency_ms_sk WHERE res = 60)\nSELECT time(w, 'unixepoch') AS minute, endpoint, round(2 * pow(${g}, min(b)) / (${g} + 1), 1) AS p99_ms\nFROM c WHERE cum >= 0.99 * tot GROUP BY w, endpoint ORDER BY p99_ms DESC LIMIT 10`],
    ['One minute, summarised', "SELECT time(w, 'unixepoch') AS minute, endpoint, n AS requests, round(ms_sum / n, 1) AS avg_ms,\n  ms_min, ms_max, an AS unusual\nFROM latency_win WHERE res = 60\nORDER BY w DESC, endpoint LIMIT 25"],
    ['Unusual requests kept', "SELECT time(ts, 'unixepoch') AS time, endpoint, ms, round(z, 1) AS z\nFROM latency_anomaly ORDER BY ts DESC LIMIT 25"],
    ['Samples', "SELECT time(ts, 'unixepoch') AS time, endpoint, ms\nFROM latency_sample WHERE res = 60\nORDER BY w DESC, endpoint, slot LIMIT 15"],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
    ['What the file says about itself', 'SELECT name, kind, detail FROM _precomputing_objects'],
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

$('traffic').value = String(DEFAULT_RATE);
setState('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
