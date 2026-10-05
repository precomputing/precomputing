// Demo 2 page: controls, live figures, candles, the race and the SQL box. The Engine runs in worker.js.
import { $, h, fmt, LineChart, CandleChart, renderResult, highlight, isEmbedded } from '../../lib/kit.js';
import { OPEN, DAY, SYMBOLS, JUMPS, HALT } from './market.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });
const SCRIPTED_PLUG = 13500;

let state = 'loading';      // loading, idle, playing, paused, finishing, done
let racing = false;
let files = null;
let view = { sym: 3, res: 60 };
let last = null;
let sample = null;          // for the rate and the busy share
let started = false;
let queryId = 0;
let compileId = 0;
const logged = new Set();
const plugRows = new Map();

// New York time: the trading day is in daylight saving time, UTC-4.
const nyTime = (ts, secs = false) => {
  const d = new Date((ts - 4 * 3600) * 1000);
  const hm = `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
  return secs ? `${hm}:${String(d.getUTCSeconds()).padStart(2, '0')}` : hm;
};
const price = (v) => (v == null ? '–' : v.toFixed(2));
const compact = (v) => (v >= 1e9 ? `${(v / 1e9).toFixed(2)}B` : v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e4 ? `${Math.round(v / 1e3)}k` : fmt.int(v));
const pctText = (x) => `${x >= 0 ? '+' : '−'}${Math.abs(x * 100).toFixed(2)}%`;

const candleChart = new CandleChart($('candles'), { aria: 'Candlestick chart of the chosen symbol, with volume.' });
const hours = [1800, 5400, 9000, 12600, 16200, 19800, 23400];
const rateChart = new LineChart($('rate-chart'), {
  height: 230, xMax: DAY, xTicks: hours, xLabel: (t) => nyTime(OPEN + t), right: 110,
  yFormat: (v, precise) => (precise ? `${fmt.int(v)} a second` : fmt.int(v)),
  series: [{ key: 'rate', name: 'All symbols', color: 'var(--teal)' }],
  aria: 'Line chart of trades a second across all symbols, minute by minute through the day.',
});

for (const [i, s] of SYMBOLS.entries()) $('sym').append(h('option', { value: i, text: s }));
$('sym').value = String(view.sym);

function domainFor(res, candles) {
  if (res === 60) {
    return { from: OPEN, to: OPEN + DAY, res, ticks: hours.map((t) => OPEN + t), label: (w) => nyTime(w),
      title: (w) => `${nyTime(w)} to ${nyTime(w + 60)}` };
  }
  if (res === 3600) {
    const from = OPEN - 1800;
    return { from, to: OPEN + DAY, res, ticks: [from, ...hours.map((t) => OPEN + t)], label: (w) => nyTime(w),
      title: (w) => `${nyTime(Math.max(w, OPEN))} to ${nyTime(w + 3600)}` };
  }
  const end = candles.length ? candles[candles.length - 1].w + 1 : OPEN + 300;
  const from = Math.max(OPEN, end - 300);
  const ticks = [];
  for (let t = from - (from % 60) + 60; t < from + 300; t += 60) ticks.push(t);
  return { from, to: from + 300, res, ticks, label: (w) => nyTime(w), title: (w) => nyTime(w, true) };
}

function setView(v) {
  view = { ...view, ...v };
  $('sym').value = String(view.sym);
  for (const r of [1, 60, 3600]) $(`res-${r}`).setAttribute('aria-pressed', String(r === view.res));
  $('btn-up').textContent = `${SYMBOLS[view.sym]} up 5%`;
  $('btn-down').textContent = `${SYMBOLS[view.sym]} down 5%`;
  worker.postMessage({ type: 'view', ...view });
  if (last) drawCandles(last);
}
$('sym').addEventListener('change', () => setView({ sym: Number($('sym').value) }));
for (const r of [1, 60, 3600]) $(`res-${r}`).addEventListener('click', () => setView({ res: r }));

function setState(s) {
  state = s;
  const busy = s === 'loading' || s === 'finishing' || racing;
  const live = s === 'playing' || s === 'paused';
  $('btn-play').disabled = busy;
  $('play-label').textContent = s === 'playing' ? 'Pause' : s === 'paused' ? 'Resume' : s === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', s === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || s === 'idle';
  $('btn-review').disabled = busy || s === 'done';
  $('btn-up').disabled = !live || racing;
  $('btn-down').disabled = !live || racing;
  $('btn-plug').disabled = !live || racing;
  $('btn-race').disabled = s === 'loading' || s === 'finishing' || racing;
  $('sql-run').disabled = !(s === 'paused' || s === 'done' || s === 'idle') || racing;
  $('sql-status').textContent = s === 'playing' ? 'Pause the day to ask the file.' : '';
  const hints = {
    idle: 'Press Play. The trading day passes in about a minute. Move a price or pull the plug while it runs.',
    playing: 'Pull the plug whenever you like, or move the chosen symbol and watch the price-jump list.',
    paused: 'Paused. You can ask the file now, run the race, or resume.',
    finishing: 'Finishing the day and checking every closed candle.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = racing ? 'The race is running; the trading day waits for it.' : hints[s] || '';
}

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) { started = true; log(0, 'The market opens: eight symbols, busiest in the first minutes.', 'teal'); }
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) { started = true; log(0, 'The market opens. Running the day at full speed.', 'teal'); }
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('btn-up').addEventListener('click', () => worker.postMessage({ type: 'jump', sym: view.sym, factor: 1.05 }));
$('btn-down').addEventListener('click', () => worker.postMessage({ type: 'jump', sym: view.sym, factor: 0.95 }));
$('btn-plug').addEventListener('click', () => worker.postMessage({ type: 'plug' }));
let raceResume = false;
$('btn-race').addEventListener('click', () => {
  racing = true;
  raceResume = state === 'playing';
  setState(raceResume ? 'paused' : state);
  worker.postMessage({ type: 'race' });
});

function resetView() {
  logged.clear();
  plugRows.clear();
  started = false;
  sample = null;
  last = null;
  $('log').replaceChildren();
  $('alerts').replaceChildren();
  $('plugs').tBodies[0].replaceChildren(h('tr', { class: 'empty' }, h('td', { colspan: 7, class: 'muted', text: 'Not yet. Press Play, then Pull the plug whenever you like.' })));
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  $('c-rate').textContent = '–';
  $('c-busy').textContent = '–';
  $('c-top').textContent = '–';
}

// log adds a line in time order, newest first, whenever it arrives.
function log(t, text, kind = '') {
  const li = h('li', { 'data-t': t }, h('span', { class: 'when', text: nyTime(OPEN + t) }), h('span', { class: `dot ${kind}` }), h('span', { text }));
  const after = [...$('log').children].find((x) => Number(x.dataset.t) <= t);
  $('log').insertBefore(li, after || null);
}

// Scripted moments, logged as the clock passes them.
const script = [
  { t: 1800, text: 'A busy stretch until 10:20.', kind: '' },
  { t: JUMPS[0].t, text: `News: ${SYMBOLS[JUMPS[0].sym]} jumps 8% in one trade.`, kind: 'amber' },
  { t: HALT.from, text: `${SYMBOLS[HALT.sym]} is halted for ten minutes.`, kind: '' },
  { t: HALT.to, text: `${SYMBOLS[HALT.sym]} reopens 3.5% lower.`, kind: 'amber' },
  { t: 10800, text: 'Lunch: trading calms down until 13:30.', kind: '' },
  { t: SCRIPTED_PLUG, text: 'The demo pulls the plug by itself during the next checkpoint.', kind: 'red' },
  { t: JUMPS[1].t, text: `News: ${SYMBOLS[JUMPS[1].sym]} drops 6% in one trade.`, kind: 'amber' },
];

function drawCandles(m) {
  const res = m.view.res;
  const title = res === 1 ? '1-second candles, the last five minutes' : res === 60 ? '1-minute candles' : '1-hour candles';
  $('chart-title').textContent = `${SYMBOLS[m.view.sym]}, ${title}`;
  candleChart.setData(m.candles, domainFor(res, m.candles));
}

function onSnapshot(m) {
  last = m;
  const t = m.t;
  $('bar').style.width = `${(100 * t) / m.day}%`;
  $('clock').textContent = t ? `${nyTime(OPEN + t, true)} New York` : '09:30:00 New York, the open';
  $('c-trades').textContent = fmt.int(m.trades);
  const now = performance.now();
  if (state === 'playing' || state === 'finishing') {
    if (sample && now - sample.at > 900) {
      const dt = now - sample.at;
      // After the close nothing moves: the last figures stay up.
      if (m.trades > sample.trades) {
        $('c-rate').textContent = fmt.int(((m.trades - sample.trades) / dt) * 1000);
        $('c-busy').textContent = `${Math.min(100, Math.round((100 * (m.engineMs - sample.engineMs)) / dt))}%`;
      }
      sample = { at: now, trades: m.trades, engineMs: m.engineMs };
    } else if (!sample) sample = { at: now, trades: m.trades, engineMs: m.engineMs };
  }
  if (m.engineMs > 50) $('c-top').textContent = fmt.int(m.trades / (m.engineMs / 1000));
  $('c-pending').textContent = fmt.int(m.pending);
  $('c-file').textContent = fmt.bytes(m.fileBytes);
  drawCandles(m);
  const tb = $('quotes').tBodies[0];
  tb.replaceChildren(...m.quotes.map((q) => {
    const i = SYMBOLS.indexOf(q.symbol);
    const chg = q.last / q.open - 1;
    return h('tr', { class: `pick${i === view.sym ? ' sel' : ''}`, onclick: () => setView({ sym: i }), title: `Chart ${q.symbol}` },
      h('td', {}, h('code', { text: q.symbol })),
      h('td', { class: 'num', text: price(q.last) }),
      h('td', { class: `num ${chg >= 0 ? 'up' : 'down'}`, text: pctText(chg) }),
      h('td', { class: 'num', text: price(q.high) }),
      h('td', { class: 'num', text: price(q.low) }),
      h('td', { class: 'num', text: compact(q.volume), title: `${fmt.int(q.volume)} shares` }),
      h('td', { class: 'num', text: (q.turnover / q.volume).toFixed(3) }),
      h('td', { class: 'num', text: compact(q.trades), title: `${fmt.int(q.trades)} trades` }));
  }));
  rateChart.setData(m.perMinute.map(([w, n]) => ({ t: w - OPEN + 60, rate: n / 60 })));
  $('alerts-sub').textContent = m.alertCount
    ? `${fmt.int(m.alertCount)} kept whole in trades_anomaly: trades whose step from the previous trade is far outside the usual steps.`
    : 'Trades whose step from the previous trade is far outside the usual steps, kept whole.';
  if (!m.alerts.length) {
    $('alerts').replaceChildren(h('li', { class: 'muted', text: `None yet. The day's first piece of news comes at ${nyTime(OPEN + JUMPS[0].t)}; the buttons above move a price of your own.` }));
  } else $('alerts').replaceChildren(...m.alerts.map((a) => h('li', {},
    h('span', { class: 'when', text: nyTime(a.ts, true) }),
    h('span', { class: 'dot amber' }),
    h('span', {}, h('code', { text: a.symbol }), ` ${price(a.before)} to ${price(a.price)}`,
      a.before ? h('strong', { class: a.price >= a.before ? 'up' : 'down', text: ` ${pctText(a.price / a.before - 1)}` }) : null,
      h('span', { class: 'muted', text: `, z ${Math.round(a.z)}` })))));
  if (started) {
    for (const s of script) {
      if (t >= s.t && !logged.has(s.t)) { logged.add(s.t); log(s.t, s.text, s.kind); }
    }
  }
  if (m.done && !logged.has('done')) { logged.add('done'); log(DAY, 'The close. The results are below.', 'teal'); }
}

function onPlug(p) {
  const who = p.by === 'you' ? 'You' : 'The demo';
  const check = p.check
    ? h('span', { class: p.check.diffs ? 'bad' : 'ok', text: `${fmt.int(p.check.checked)}: ${p.check.diffs ? `${p.check.diffs} differ` : 'all identical'}` })
    : h('span', { class: 'muted', text: 'once the file has caught up' });
  const row = h('tr', {},
    h('td', { text: nyTime(OPEN + p.t, true) }),
    h('td', { text: who }),
    h('td', { class: 'num', text: fmt.int(p.lost) }),
    h('td', { class: 'num', text: fmt.int(p.cutRows) }),
    h('td', { class: 'num', text: fmt.int(p.resent) }),
    h('td', { class: 'num', text: fmt.time(p.recoverMs) }),
    h('td', {}, check));
  const tb = $('plugs').tBodies[0];
  tb.querySelector('tr.empty')?.remove();
  const old = plugRows.get(p.i);
  if (old) old.replaceWith(row); else { tb.append(row); log(p.t, `${who} pulled the plug: ${fmt.int(p.lost)} trades were only in memory, and ${fmt.int(p.cutRows)} rows of a half-written checkpoint were rolled back. A fresh Engine opened the file and had every trade back in ${fmt.time(p.recoverMs)}.`, 'red'); }
  plugRows.set(p.i, row);
  if (p.check && !logged.has(`check${p.i}`)) {
    logged.add(`check${p.i}`);
    log(p.check.upTo - OPEN, `After the plug: ${fmt.int(p.check.checked)} candles checked against a recount of every trade, ${p.check.diffs ? `${p.check.diffs} differ` : 'all identical'}.`, p.check.diffs ? 'red' : 'teal');
  }
}

function onRace(m) {
  const rate = (x) => (x.ms > 0 && x.trades ? `${fmt.int(x.trades / (x.ms / 1000))} a second` : '–');
  $('race-e').style.width = `${(100 * m.engine.trades) / m.total}%`;
  $('race-s').style.width = `${(100 * m.sql.trades) / m.total}%`;
  $('race-ev').textContent = rate(m.engine);
  $('race-sv').textContent = rate(m.sql);
  if (m.phase === 'start') {
    $('race-sub').textContent = `The first ${fmt.int(m.total)} trades of the day, through a fresh Engine and through the same policy compiled to SQLite triggers. Both take turns on the same thread in equal slices of time, then every value in the two files is compared.`;
    $('race-status').textContent = 'Racing.';
    $('race-out').replaceChildren();
  }
  if (m.phase === 'compare') $('race-status').textContent = 'Comparing every value in the two files.';
  if (m.phase === 'done') {
    racing = false;
    setState(raceResume && state === 'paused' ? 'playing' : state);
    const rows = m.tables.reduce((a, t) => a + t.rows, 0);
    const values = m.tables.reduce((a, t) => a + t.values, 0);
    const same = m.tables.every((t) => !t.diffs);
    const x = m.sql.ms / m.engine.ms;
    $('race-status').textContent = '';
    $('race-out').replaceChildren(
      h('p', {}, h('strong', { text: `The Engine was ${fmt.times(x)} faster. ` }),
        `It took the ${fmt.int(m.total)} trades in at ${rate(m.engine)}, the triggers at ${rate(m.sql)}, on the same thread. `,
        same ? h('strong', { class: 'ok', text: `The two files hold the same ${fmt.int(rows)} rows, and all ${fmt.int(values)} values are identical, bit for bit.` })
          : h('strong', { class: 'bad', text: 'The files differ.' })),
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', { text: 'Table' }), h('th', { class: 'num', text: 'Rows' }), h('th', { text: 'Engine and SQL' }))),
        h('tbody', {}, m.tables.map((t) => h('tr', {}, h('td', {}, h('code', { text: t.name })), h('td', { class: 'num', text: fmt.int(t.rows) }),
          h('td', {}, h('span', { class: t.diffs ? 'bad' : 'ok', text: t.diffs ? `${t.diffs} differ` : 'identical' }))))))));
  }
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const perSec = r.trades / (r.engineMs / 1000);
  $('c-top').textContent = fmt.int(perSec);
  $('c-file').textContent = fmt.bytes(r.fileBytes);
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.trades)} trades in a trading day. ` }),
    `On this device the Engine took them in at ${fmt.int(perSec)} a second while writing its file ${fmt.int(r.checkpoints)} times, every 0.2 seconds. `,
    `The file holds 1-minute and 1-hour candles for the whole day, 1-second candles for the last hour, the last five minutes of trades, the quote board and the price jumps in ${fmt.bytes(r.fileBytes)}. `,
    `A plain table of every trade would take about ${fmt.bytes(r.plainBytes)}.`,
    r.plugs.length ? ` The plug was pulled ${r.plugs.length === 1 ? 'once' : `${r.plugs.length} times`}; not one trade was lost or counted twice.` : '');
  const b = r.check.byRes;
  const ok = (d) => h('span', { class: d ? 'bad' : 'ok', text: d ? `${d} differ` : 'identical' });
  const rows = [
    ['1-minute candles, every closed minute', b[60].candles, b[60].diffs],
    ['1-hour candles, every closed hour', b[3600].candles, b[3600].diffs],
    ['1-second candles, last 50 min', b[1].candles, b[1].diffs],
    ['Quote board rows', SYMBOLS.length, r.quoteDiffs],
    ['Trades kept whole, last 5 min', r.rawRows, r.rawDiffs],
    ['Trades counted', r.total, r.total === r.trades && r.seq === r.trades && r.events === r.trades ? 0 : 1],
  ];
  $('checks').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'What' }), h('th', { class: 'num', text: 'Checked' }), h('th', { text: 'Result' }))),
    h('tbody', {}, rows.map(([what, n, d]) => h('tr', {}, h('td', { text: what }), h('td', { class: 'num', text: fmt.int(n) }), h('td', {}, ok(d))))));
  $('checks-note').textContent = `A candle is closed once a later trade has arrived, so the day's last minute and last hour are left out. A candle is checked on its count, open, high, low, close, volume, turnover and sum of prices; a quote board row on its last price, open, high, low, volume, turnover and trades. Every value is compared exactly. `
    + `${r.alerts.length} trades were flagged as price jumps: ${r.alerts.slice().reverse().map((a) => `${a.symbol} at ${nyTime(a.ts, true)}`).join(', ')}.`;
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table or index' }), h('th', { class: 'num', text: 'Rows' }), h('th', { class: 'num', text: 'Size' }))),
    h('tbody', {}, r.tables.map((t) => h('tr', {}, h('td', {}, h('code', { text: t.name })), h('td', { class: 'num', text: t.rows == null ? '' : fmt.int(t.rows) }), h('td', { class: 'num', text: fmt.bytes(t.bytes) })))));
  $('file-note').textContent = `trades_win holds the candles; trades_raw and its time index the last five minutes of trades. The plain-table size is measured on the first 100,000 trades (${r.plainPerTrade.toFixed(1)} bytes each) and scaled to the day.`;
  $('download-row').hidden = host === 'artifact';
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      $('sqlite-version').textContent = m.sqlite;
      $('pc-version').textContent = m.version;
      showCode('policy');
      $('compiler-src').value = m.policy;
      buildPresets();
      setState('idle');
      setView({});
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'plug': onPlug(m.plug); break;
    case 'race': onRace(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'event': log(m.t, m.text, 'amber'); break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'compiled': if (m.id === compileId) showCompiled(m); break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'trades.sqlite' });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      break;
    }
    case 'error':
      racing = false;
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
$('compiler-run').addEventListener('click', () => {
  compileId++;
  $('compiler-status').textContent = 'Compiling…';
  worker.postMessage({ type: 'compile', id: compileId, text: $('compiler-src').value });
});
function showCompiled(m) {
  const out = $('compiler-out');
  out.hidden = false;
  if (m.error) {
    out.replaceChildren(h('span', { class: 'bad', text: `Line ${m.error}` }));
    $('compiler-status').textContent = 'The policy has a problem.';
  } else {
    highlight(out, m.schema, 'sql');
    $('compiler-status').textContent = `Compiled in ${fmt.time(m.ms)}: ${m.schema.split('\n').length} lines of SQL.`;
  }
}

// Ask the file
function buildPresets() {
  const ny = (col) => `time(${col}, 'unixepoch', '-4 hours')`;
  const presets = [
    ['Quote board', 'SELECT l.symbol, l.value AS last, o.value AS open, h.value AS high, lo.value AS low,\n  v.value AS volume, round(t.value / v.value, 3) AS vwap, n.value AS trades\nFROM last_price l JOIN day_open o USING (symbol) JOIN day_high h USING (symbol, period)\n  JOIN day_low lo USING (symbol, period) JOIN day_volume v USING (symbol, period)\n  JOIN day_turnover t USING (symbol, period) JOIN day_trades n USING (symbol, period)\nORDER BY volume DESC'],
    ['SIM4 around its jump', `SELECT ${ny('w')} AS new_york, price_first AS open, price_max AS high, price_min AS low,\n  price_last AS close, size_sum AS volume, round(notional_sum / size_sum, 3) AS vwap, n AS trades, an AS unusual\nFROM trades_win WHERE res = 60 AND symbol = 'SIM4' AND w BETWEEN ${OPEN + JUMPS[0].t - 360} AND ${OPEN + JUMPS[0].t + 300}\nORDER BY w`],
    ['Hourly candles', `SELECT ${ny('w')} AS new_york, symbol, price_first AS open, price_max AS high, price_min AS low,\n  price_last AS close, size_sum AS volume\nFROM trades_win WHERE res = 3600 ORDER BY symbol, w`],
    ['Busiest minutes', `SELECT ${ny('w')} AS new_york, sum(n) AS trades, sum(size_sum) AS shares\nFROM trades_win WHERE res = 60 GROUP BY w ORDER BY trades DESC LIMIT 10`],
    ['Price jumps', `SELECT ${ny('ts')} AS new_york, symbol, price, size, round(z) AS z\nFROM trades_anomaly ORDER BY ts`],
    ['The last trades', `SELECT ${ny('ts')} AS new_york, symbol, price, size\nFROM trades_raw ORDER BY rowid DESC LIMIT 20`],
    ['Where the Engine is', 'SELECT source, seq AS last_trade_in_file, events, datetime(newest, \'unixepoch\', \'-4 hours\') AS newest_new_york\nFROM _precomputing_sources'],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
    ['What the file says about itself', 'SELECT name, kind, detail FROM _precomputing_objects'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[0][1];
}
function runSql() {
  if ($('sql-run').disabled) { $('sql-status').textContent = racing ? 'The race is running.' : 'Pause the day to ask the file.'; return; }
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

setState('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
