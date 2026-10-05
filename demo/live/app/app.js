// Demo 5 page: Wikimedia's stream of recent changes read here and handed to the Engine in
// worker.js; the chart, what people are editing, the busiest wikis, the checks and the SQL box
// read the Engine's file.
import { $, h, fmt, renderResult, highlight, isEmbedded, LineChart } from '../../lib/kit.js';
import { STREAM_URL, pageUrl } from './wiki.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });
const params = new URLSearchParams(location.search);
const streamUrl = params.get('stream') || STREAM_URL;
const streamHost = (() => { try { return new URL(streamUrl).host; } catch { return streamUrl; } })();

let mode = 'loading';           // loading, idle, connecting, live, paused, failed, sim
let files = null;
let es = null;
let openTimer = null;
let pending = [];
let queryId = 0;
let last = null;
let received = 0;
const KIND = { edit: 'Edits', new: 'New pages', log: 'Log entries: uploads, moves, blocks and more', categorize: 'Pages added to or taken out of categories', external: 'Changes from other wikis' };

const clockLabel = (ts) => new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const chart = new LineChart($('chart'), {
  height: 230, xMin: 0, xMax: 1800, xTicks: [], xLabel: (t) => (t === chart.o.xMax ? 'now' : clockLabel(t)), tipLabel: (t) => `10 seconds to ${new Date(t * 1000).toLocaleTimeString()}`,
  yFormat: (v) => fmt.int(v), aria: 'Changes every ten seconds by people and by bots',
  series: [{ key: 'people', name: 'By people', color: 'var(--teal)' }, { key: 'bots', name: 'By bots', color: 'var(--gray-2)' }],
});

function setMode(m, detail) {
  mode = m;
  const ready = m !== 'loading';
  const on = m === 'connecting' || m === 'live';
  $('btn-live').disabled = !ready;
  $('live-label').textContent = on ? 'Pause' : m === 'paused' ? 'Reconnect' : 'Connect to Wikipedia';
  $('live-icon').setAttribute('d', on ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-sim').disabled = !ready;
  $('sim-label').textContent = m === 'sim' ? 'Stop the simulation' : 'Watch simulated changes';
  $('btn-clear').disabled = !ready;
  $('btn-check').disabled = !ready;
  $('sql-run').disabled = !ready;
  $('btn-download').disabled = !ready || host === 'artifact';
  $('btn-download').hidden = host === 'artifact';
  $('pulse').className = `pulse ${m === 'live' ? 'on' : m === 'sim' ? 'sim' : m === 'failed' ? 'bad' : ''}`;
  const text = {
    loading: 'Loading SQLite and the Engine',
    idle: 'Not connected',
    connecting: `Connecting to ${streamHost}…`,
    live: `Live from ${streamHost}`,
    paused: 'Paused: nothing is received',
    failed: `Can't reach ${streamHost}`,
    sim: 'Simulated changes, invented here',
  }[m];
  $('source-text').textContent = text;
  const hints = {
    idle: 'Press Connect. Changes from every Wikimedia wiki arrive a few seconds after they are made, usually tens of them a second.',
    connecting: 'Connecting. The first changes usually arrive within a few seconds.',
    live: 'Live. Every change goes into the file; everything below is read from it each second. Pick a wiki, or pause and ask the file.',
    paused: 'Paused. The file keeps what it has; changes made meanwhile are not received.',
    sim: 'These changes are invented here, in the shape of the real stream, with a few people editing one article now and then. Nothing below is real.',
  };
  $('hint').replaceChildren(m === 'failed' ? h('span', {}, h('span', { class: 'bad', text: detail || `The stream can't be reached from this page.` }), ' ',
    'Your network or the page\'s host may block it. You can watch simulated changes instead.') : (hints[m] || ''));
}

function connect() {
  disconnect();
  worker.postMessage({ type: 'sim', on: false });
  try {
    es = new EventSource(streamUrl);
  } catch (e) {
    setMode('failed', `The stream could not be opened (${e.message}).`);
    return;
  }
  setMode('connecting');
  openTimer = setTimeout(() => { if (es && es.readyState !== 1) { disconnect(); setMode('failed', 'No answer from the stream after 15 seconds.'); } }, 15000);
  es.onopen = () => { clearTimeout(openTimer); setMode('live'); };
  es.onmessage = (ev) => { received++; pending.push(ev.data); if (mode === 'connecting') { clearTimeout(openTimer); setMode('live'); } };
  let tries = 0;
  es.onerror = () => {
    if (!es) return;
    if (es.readyState === EventSource.CLOSED) { disconnect(); setMode('failed', 'The stream closed the connection.'); }
    else if (mode === 'live') setMode('connecting');
    else if (mode === 'connecting' && received === 0 && ++tries >= 3) { disconnect(); setMode('failed', 'The stream did not answer.'); }
  };
}

function disconnect() {
  clearTimeout(openTimer);
  if (es) { es.onopen = es.onmessage = es.onerror = null; es.close(); es = null; }
}

document.addEventListener('securitypolicyviolation', (e) => {
  if (es && String(e.blockedURI || '').includes(streamHost)) { disconnect(); setMode('failed', `This page's host does not allow connections to ${streamHost}.`); }
});

setInterval(() => {
  if (pending.length) { worker.postMessage({ type: 'lines', lines: pending }); pending = []; }
}, 250);

// The file holds changes from one source at a time: 'live', 'sim', or null while it is empty.
// Switching source starts a new file, so real and invented changes never mix.
let fileSource = null;
function useSource(src) {
  if (fileSource && fileSource !== src) { worker.postMessage({ type: 'clear' }); resetView(); }
  fileSource = src;
}
$('btn-live').addEventListener('click', () => {
  if (mode === 'connecting' || mode === 'live') { disconnect(); setMode('paused'); return; }
  worker.postMessage({ type: 'sim', on: false });
  useSource('live');
  connect();
});
$('btn-sim').addEventListener('click', () => {
  if (mode === 'sim') { worker.postMessage({ type: 'sim', on: false }); setMode('paused'); return; }
  disconnect();
  useSource('sim');
  worker.postMessage({ type: 'sim', on: true });
  setMode('sim');
});
$('btn-clear').addEventListener('click', () => {
  const was = mode;
  worker.postMessage({ type: 'clear' });
  resetView();
  fileSource = was === 'sim' ? 'sim' : was === 'live' || was === 'connecting' ? 'live' : null;
  if (was === 'sim') { worker.postMessage({ type: 'sim', on: true }); }
});
$('wiki').addEventListener('change', () => worker.postMessage({ type: 'filter', filter: $('wiki').value }));
$('btn-check').addEventListener('click', () => { $('check-status').textContent = 'Checking…'; worker.postMessage({ type: 'check' }); });

function resetView() {
  received = 0;
  pending = [];
  last = null;
  $('sql-out').replaceChildren();
  $('check-status').textContent = '';
}

function drawWikiList(list) {
  const sel = $('wiki');
  const cur = sel.value;
  const opts = [['all', 'every wiki'], ['wikipedias', 'every Wikipedia'], ...list.slice(0, 30).map((x) => [x.wiki, x.wiki])];
  if (!opts.some(([v]) => v === cur)) opts.push([cur, cur]);
  sel.replaceChildren(...opts.map(([v, t]) => h('option', { value: v, text: t })));
  sel.value = cur;
}

function onSnapshot(m) {
  last = m;
  $('c-changes').textContent = fmt.int(m.counts.events);
  $('c-rate').textContent = m.t ? m.rate.toFixed(1) : '–';
  $('c-bots').textContent = m.recent ? `${Math.round(m.bots * 100)}%` : '–';
  $('c-file').textContent = fmt.bytes(m.fileBytes);
  if (m.wikiList) { $('c-wikis').textContent = fmt.int(m.wikiList.length); drawWikiList(m.wikiList); }
  const ck = m.checked;
  $('c-checked').textContent = ck ? fmt.int(ck.same) : '–';
  $('c-checked').parentElement.classList.toggle('good', !ck || ck.same === ck.cells);
  $('c-checked').nextElementSibling.textContent = ck && ck.same !== ck.cells ? `counts checked, ${fmt.int(ck.cells - ck.same)} differ` : 'counts checked, all equal';
  if (ck) {
    const c = m.check;
    $('check-line').replaceChildren(h('span', { class: ck.same === ck.cells ? 'ok' : 'bad', text: ck.same === ck.cells ? '✓ ' : '✗ ' }),
      h('strong', { text: `${fmt.int(ck.same)} of ${fmt.int(ck.cells)}` }), ` counts and sums equal the recount, in ${fmt.int(ck.checks)} check${ck.checks === 1 ? '' : 's'}.`,
      c ? h('span', { class: 'muted', text: ` The last one read ${fmt.int(c.cells)} in ${fmt.time(c.ms)}.` }) : '',
      ck.first ? h('span', { class: 'bad', text: ` First difference: ${ck.first}` }) : '');
  }
  $('source-note').textContent = `${fmt.int(m.counts.canaries)} of the stream's own test events skipped, ${fmt.int(m.counts.repeats)} changes that arrived twice counted once, ${fmt.int(m.counts.unread)} lines that could not be read. ` +
    'No user names or edit summaries are shown or kept. People are counted by a number made from the user name and a random value picked when this page opened.';
  // The chart: complete 10-second windows of the last half hour.
  if (m.t) {
    // The last half hour, or as much as there is, at least five minutes.
    const end = m.t - (m.t % 10);
    const firstFull = m.first - (m.first % 10) + 10;
    const span = Math.max(300, Math.min(1800, end - firstFull));
    const step = [60, 120, 300, 600].find((x) => span / x <= 5) || 600;
    chart.o.xMin = end - span;
    chart.o.xMax = end;
    const ticks = [];
    for (let x = Math.ceil((end - span) / step) * step; x < end; x += step) if (x - (end - span) >= 0.5 * step && end - x >= step) ticks.push(x);
    ticks.push(end);
    chart.o.xTicks = ticks;
    chart.setData(m.chart.filter(([w]) => w + 10 <= end).map(([w, people, bots]) => ({ t: w + 10, people, bots })));
  } else chart.setData([]);
  $('trending').tBodies[0].replaceChildren(...(m.trending.length ? m.trending.map(([wiki, title, people, edits, bytes]) => h('tr', {},
    h('td', {}, h('a', { href: pageUrl(wiki, title), target: '_blank', rel: 'noopener', text: title })),
    h('td', { class: 'muted', text: wiki.replace(/\.org$/, '') }), h('td', { class: 'num strong', text: String(people) }), h('td', { class: 'num', text: String(edits) }),
    h('td', { class: `num ${bytes < 0 ? 'down' : ''}`, text: `${bytes > 0 ? '+' : ''}${fmt.int(bytes)}` })))
    : [h('tr', {}, h('td', { colspan: 5, class: 'muted', text: m.t ? 'No article has two or more people editing it yet.' : 'Nothing yet.' }))]));
  const max = m.wikis.length ? m.wikis[0][1] : 1;
  $('wikis').replaceChildren(...(m.wikis.length ? m.wikis.map(([wiki, n, bots]) => h('div', { class: 'barrow wide' },
    h('span', { class: 'k', text: wiki.replace(/\.org$/, '') }), h('span', { class: 'b' }, h('i', { style: `width:${(100 * n) / max}%` }), h('i', { class: 'bot', style: `width:${(100 * bots) / max}%` })),
    h('span', { class: 'v', text: `${fmt.int(n)} · ${Math.round((100 * bots) / n)}% bots` })))
    : [h('p', { class: 'muted', text: 'Nothing yet.' })]));
  $('kinds').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Kind' }), h('th', { class: 'num', text: 'Changes' }), h('th', { class: 'num', text: 'By bots' }))),
    h('tbody', {}, m.kinds.length ? m.kinds.map(([kind, n, bots]) => h('tr', {}, h('td', { text: KIND[kind] || kind }), h('td', { class: 'num', text: fmt.int(n) }), h('td', { class: 'num', text: `${Math.round((100 * bots) / n)}%` })))
      : [h('tr', {}, h('td', { colspan: 3, class: 'muted', text: 'Nothing yet.' }))]));
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      $('sqlite-version').textContent = m.sqlite;
      $('pc-version').textContent = m.version;
      showCode('policy');
      buildPresets();
      setMode('idle');
      if (params.get('autostart') === 'sim') $('btn-sim').click();
      else if (params.get('autostart') === 'live') $('btn-live').click();
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'check': $('check-status').textContent = m.result ? `Checked ${fmt.int(m.result.cells)} in ${fmt.time(m.result.ms)}.` : 'Nothing to check yet.'; break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'wikipedia-live.sqlite' });
      document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      break;
    }
    case 'error':
      $('hint').replaceChildren(h('span', { class: 'bad', text: m.message }));
      if (mode === 'loading') $('source-text').textContent = 'Could not start';
      break;
  }
};
worker.onerror = (e) => {
  $('hint').replaceChildren(h('span', { class: 'bad', text: `The demo could not start in this browser (${e.message || 'worker error'}). It needs a current Chrome, Edge, Firefox or Safari.` }));
  $('source-text').textContent = 'Could not start';
};

function showCode(which) {
  for (const id of ['policy', 'sql']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  if (!files) return;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'sql') highlight($('code'), files.schema + '\n' + files.distill, 'sql');
}
for (const id of ['policy', 'sql']) $(`tab-${id}`).addEventListener('click', () => showCode(id));

function buildPresets() {
  const presets = [
    ['Changes a minute, by wiki', "SELECT time(w, 'unixepoch') AS minute_utc, wiki, sum(n) AS changes,\n  sum(CASE WHEN who = 'bot' THEN n ELSE 0 END) AS by_bots\nFROM edits_win WHERE res = 60\nGROUP BY w, wiki ORDER BY w DESC, changes DESC LIMIT 40"],
    ['People on one article', "SELECT wiki, title, count(DISTINCT editor) AS people, sum(n) AS edits\nFROM article_editors_win WHERE res = 60\nGROUP BY wiki, title ORDER BY people DESC, edits DESC LIMIT 20"],
    ['Biggest additions, last hour', "SELECT wiki, title, sum(n) AS edits, sum(bytes_sum) AS bytes_added\nFROM article_edits_win WHERE res = 60\nGROUP BY wiki, title ORDER BY bytes_added DESC LIMIT 20"],
    ['Biggest removals, last hour', "SELECT wiki, title, sum(n) AS edits, sum(bytes_sum) AS bytes_added\nFROM article_edits_win WHERE res = 60\nGROUP BY wiki, title ORDER BY bytes_added LIMIT 20"],
    ['Today, by wiki', 'SELECT wiki, period AS day_utc, value AS changes FROM changes_day\nORDER BY value DESC LIMIT 20'],
    ['The last ten minutes, change by change', "SELECT time(ts, 'unixepoch') AS utc, wiki, kind, who, bytes\nFROM edits ORDER BY ts DESC LIMIT 50"],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[1][1];
}
function runSql() {
  if ($('sql-run').disabled) return;
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

setMode('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
