// Demo 6 page: what stands out right now, the closest incidents in the file, the two hours on one
// line, the incidents written down, the results and the SQL box. The reducer and the Engine run
// in worker.js.
import { $, h, s, fmt, renderResult, highlight, isEmbedded } from '../../lib/kit.js';
import { TODAY_START, TODAY_DURATION } from './scenario.js';
import { MATCH, PARTLY, CHECK_EVERY, when } from './run.js';

const host = window.PRECOMPUTING_HOST || 'site';
// A hosted preview serves no gzip files, so there the day before travels as base64 text.
const worker = new Worker(new URL(host === 'artifact' ? 'worker.js#artifact' : 'worker.js', import.meta.url), { type: 'module' });

let state = 'loading';
let files = null;
let last = null;
let started = false;
let queryId = 0, labelId = 0;
let checks = [];                // every check where something stood out: [t, state, pick, similarity]
let formEpisode = null;
let holdForm = 0;               // the form stays as it is until then, to show what happened to it
const incidentsById = new Map();

const hms = (t) => { const x = (TODAY_START + t) % 86400; return `${String(Math.floor(x / 3600)).padStart(2, '0')}:${String(Math.floor(x / 60) % 60).padStart(2, '0')}:${String(x % 60).padStart(2, '0')}`; };
const hm = (t) => hms(t).slice(0, 5);
const rate = (v) => (v >= 100 ? String(Math.round(v)) : v.toFixed(1));
const levelClass = (lv) => ({ ERROR: 'lv-error', WARN: 'lv-warn', DEBUG: 'lv-debug' })[lv] || 'lv-info';
const COLOR = { match: 'var(--teal)', partly: 'var(--amber)', new: 'var(--violet)' };
const SHORT = { payments: (x) => x.provider, attack: () => 'attack', search: () => 'search', dbpool: () => 'db pool', cache: () => 'cache', tls: () => 'certificate', queue: () => 'queue' };

function templateEl(text) {
  const parts = String(text || '').split('<*>');
  const out = [];
  parts.forEach((x, i) => { if (i) out.push(h('span', { class: 'wild', text: '<*>' })); if (x) out.push(x); });
  return h('code', {}, ...out);
}

function lineEl(line) {
  const m = /^(\S+)\s+(\S+)\s+(\S+)\s+(.*)$/.exec(line);
  if (!m) return h('div', { class: 'ln', text: line });
  return h('div', { class: 'ln' }, h('span', { class: 'ts', text: m[1].slice(11, 23) }), ' ', h('span', { class: `lv ${levelClass(m[2])}`, text: m[2].padEnd(5) }), ' ',
    h('span', { class: 'svc', text: m[3] }), ' ', m[4]);
}

// The two hours on one line: the incidents above, what the file said at each check below.
class Timeline {
  constructor(el) {
    this.el = el;
    this.truth = [];
    this.episodes = [];
    this.t = 0;
    new ResizeObserver(() => this.render()).observe(el);
  }

  set(truth, t, episodes = []) { this.truth = truth; this.t = t; this.episodes = episodes; this.render(); }

  render() {
    const w = Math.max(280, this.el.clientWidth);
    const narrow = w < 560;
    const H = 128;
    const x0 = narrow ? 4 : 132, x1 = w - 6;
    const X = (t) => x0 + (Math.max(0, Math.min(TODAY_DURATION, t)) / TODAY_DURATION) * (x1 - x0);
    const svg = s('svg', { viewBox: `0 0 ${w} ${H}`, height: H, role: 'img', 'aria-label': this.el.getAttribute('aria-label') });
    const rows = [[14, 42, 'What happened'], [62, 90, 'What the file said']];
    for (const [top, bot, name] of rows) {
      svg.append(s('rect', { x: x0, y: top, width: x1 - x0, height: bot - top, fill: 'var(--zebra)' }));
      if (!narrow) svg.append(s('text', { x: 0, y: (top + bot) / 2 + 4, class: 'label' }, name));
    }
    for (let t = 0; t <= TODAY_DURATION; t += 1800) {
      svg.append(s('line', { x1: X(t), x2: X(t), y1: 10, y2: 94, stroke: 'var(--grid)', 'stroke-width': 1 }));
      svg.append(s('text', { x: X(t), y: H - 12, 'text-anchor': t === 0 ? 'start' : t === TODAY_DURATION ? 'end' : 'middle' }, hm(t)));
    }
    for (const x of this.truth) {
      const to = Math.min(x.at + x.minutes * 60, this.t);
      if (to <= x.at) continue;
      const r = s('rect', { x: X(x.at), y: 18, width: Math.max(2, X(to) - X(x.at)), height: 20, rx: 2, fill: 'var(--red)' });
      r.append(s('title', {}, `${hms(x.at)} ${x.name}${x.by === 'you' ? ', started by you' : ''}`));
      svg.append(r);
      if (!narrow) svg.append(s('text', { x: X(x.at), y: 12, class: 'tl-name' }, SHORT[x.kind] ? SHORT[x.kind](x) : x.kind));
    }
    // Runs of checks that said the same thing, drawn as one bar each.
    let run = null;
    const flush = () => { if (run) svg.append(s('rect', { x: X(run.from), y: 66, width: Math.max(1.5, X(run.to) - X(run.from)), height: 20, fill: COLOR[run.st] })); };
    for (const [t, st] of checks) {
      if (run && run.st === st && t - run.to <= CHECK_EVERY) run.to = t;
      else { flush(); run = { st, from: t - CHECK_EVERY, to: t }; }
    }
    flush();
    for (const e of this.episodes) {
      if (!e.label || e.label.at == null) continue;
      const x = X(e.label.at);
      const mark = s('path', { d: `M${x} 58l5 -7h-10z`, fill: 'var(--navy)' });
      mark.append(s('title', {}, `${hms(e.label.at)} written down as #${e.label.id}, ${e.label.title}`));
      svg.append(mark);
      if (!narrow) svg.append(s('text', { x: x + 7, y: 57, class: 'tl-mark' }, `#${e.label.id} written down`));
    }
    if (this.t > 0 && this.t < TODAY_DURATION) svg.append(s('line', { x1: X(this.t), x2: X(this.t), y1: 10, y2: 94, stroke: 'var(--navy)', 'stroke-width': 1.5 }));
    this.el.querySelector('svg')?.remove();
    this.el.prepend(svg);
  }
}
const timeline = new Timeline($('timeline'));

function setState(st) {
  state = st;
  const busy = st === 'loading' || st === 'finishing';
  $('btn-play').disabled = busy;
  $('play-label').textContent = st === 'playing' ? 'Pause' : st === 'paused' ? 'Resume' : st === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', st === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || st === 'idle';
  $('btn-review').disabled = busy || st === 'done';
  $('btn-start').disabled = !(st === 'idle' || st === 'playing' || st === 'paused');
  $('label-save').disabled = !(st === 'playing' || st === 'paused');
  $('sql-run').disabled = !(st === 'paused' || st === 'done' || st === 'idle');
  $('sql-status').textContent = st === 'playing' ? 'Pause the run to ask the file.' : '';
  const hints = {
    idle: 'Press Play. Two hours pass in about a minute and a half, slowing down as each incident begins. Start one of your own at any moment.',
    playing: 'Watch Right now as each incident begins. Start one of your own: a kind never seen is flagged as new, and once written down it is recognized the next time.',
    paused: 'Paused. You can ask the file now, write an incident down, or resume.',
    finishing: 'Finishing the two hours at full speed, then checking every count.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = hints[st] || '';
}

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) { started = true; log(0, '12:00: the shop carries on from yesterday. The file already holds its last 24 hours and six incidents the on-call team wrote down.', 'teal'); }
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) { started = true; log(0, 'The shop carries on from yesterday. Running the two hours at full speed.', 'teal'); }
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('btn-start').addEventListener('click', () => {
  const [kind, provider] = $('kind').value.split(':');
  const opts = kind === 'payments' ? { provider } : kind === 'tls' ? { host: 'img.cdn-v.example' } : {};
  worker.postMessage({ type: 'start', kind, opts });
});

function resetView() {
  started = false;
  last = null;
  checks = [];
  formEpisode = null;
  $('log').replaceChildren();
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  $('c-rate').textContent = '–';
  $('label-form').hidden = true;
  $('label-status').textContent = '';
  timeline.set([], 0);
}

function log(t, text, kind = '') {
  const li = h('li', { 'data-t': t }, h('span', { class: 'when', text: hms(t) }), h('span', { class: `dot ${kind}` }), h('span', { text }));
  const after = [...$('log').children].find((x) => Number(x.dataset.t) <= t);
  $('log').insertBefore(li, after || null);
}

function changeText(f) {
  if (f.usual < 0.5) return 'from nothing';
  if (f.now < 0.5) return 'silent';
  return f.now >= f.usual ? `${fmt.times(f.now / f.usual)} more` : `${fmt.times(f.usual / f.now)} fewer`;
}

function drawNow(c) {
  const pick = c.matches.find((m) => m.id === c.pick);
  $('status').className = `status ${c.state}`;
  const head = $('st-head'), body = $('st-body');
  if (c.state === 'quiet') {
    head.textContent = 'Quiet';
    body.replaceChildren(h('p', { text: 'Nothing stands out from the hour before.' }));
  } else if (c.state === 'new') {
    head.textContent = 'Something new';
    body.replaceChildren(h('p', { text: 'Nothing in the file looks like this. Write it down, and next time it will be recognized.' }));
  } else if (c.state === 'partly') {
    head.textContent = `Partly like #${pick.id}, ${pick.title}`;
    body.replaceChildren(h('p', { text: `Similarity ${pick.similarity.toFixed(2)}. A little more of it will say whether it is the same.` }));
  } else {
    head.textContent = `Looks like #${pick.id}, ${pick.title}`;
    const kids = [h('p', { class: 'meta', text: `${when(pick.started)} UTC · similarity ${pick.similarity.toFixed(2)} · written down by ${pick.source === 'you' ? 'you' : `the ${pick.source}`}` }),
      h('p', {}, h('strong', { text: 'What fixed it: ' }), pick.fix)];
    if (c.deciding && c.deciding.words.length) {
      kids.push(h('p', { class: 'words' }, `Told apart from #${c.deciding.other} by the raw lines: `, ...c.deciding.words.map((w) => h('code', { text: w }))));
    }
    body.replaceChildren(...kids);
  }
  $('fp').tBodies[0].replaceChildren(...(c.fp.length ? c.fp.map((f) => h('tr', {},
    h('td', { class: 'tpl' }, h('span', { class: 'svc-name', text: f.service }), ' ', h('span', { class: `lv ${levelClass(f.level)}`, text: f.level }), ' ', templateEl(f.text)),
    h('td', { class: 'num', text: rate(f.now) }), h('td', { class: 'num', text: rate(f.usual) }),
    h('td', { class: `num ${f.score > 0 ? 'up-text' : 'down-text'}`, text: changeText(f) })))
    : [h('tr', {}, h('td', { colspan: 4, class: 'muted', text: 'Nothing stands out.' }))]));
  if (!c.fp.length) {
    $('matches').replaceChildren(h('p', { class: 'muted', text: 'Nothing stands out, so there is nothing to compare. The closest incidents appear here the moment something does.' }));
    return;
  }
  if (!c.matches.length || c.matches[0].similarity <= 0) {
    $('matches').replaceChildren(h('p', { class: 'muted', text: 'No incident in the file shares a template with what stands out now.' }));
    return;
  }
  $('matches').replaceChildren(...c.matches.filter((m) => m.similarity > 0).map((m) => h('div', { class: `match${m.id === c.pick ? ' pick' : ''}` },
    h('div', { class: 'm-top' }, h('span', { class: 'm-id', text: `#${m.id}` }), h('span', { class: 'm-title', text: m.title })),
    h('div', { class: 'm-meta', text: `${when(m.started)} UTC · ${m.shared} of ${m.keys} template${m.keys === 1 ? '' : 's'} in common` }),
    h('div', { class: 'simbar', title: `Similarity ${m.similarity.toFixed(3)}` },
      h('span', { class: 'track' }, h('i', { style: `width:${Math.max(0, Math.min(1, m.similarity)) * 100}%;background:${m.similarity >= MATCH ? 'var(--teal)' : m.similarity >= PARTLY ? 'var(--amber)' : 'var(--gray-2)'}` }),
        h('b', { class: 'tick', style: `left:${PARTLY * 100}%` }), h('b', { class: 'tick', style: `left:${MATCH * 100}%` })),
      h('span', { class: 'v', text: m.similarity.toFixed(2) })))));
}

// The form to write down the newest episode, while nothing in the file has named it.
function drawForm(m) {
  if (performance.now() < holdForm) return;
  const e = m.episodes[m.episodes.length - 1];
  const show = e && !e.label && !e.pending && e.states.match === 0 && !m.done;
  if (!show) { $('label-form').hidden = true; return; }
  if (formEpisode !== e.id) {
    formEpisode = e.id;
    $('label-title').value = '';
    $('label-fix').value = '';
    $('label-status').textContent = '';
    $('label-save').disabled = !(state === 'playing' || state === 'paused');
  }
  $('label-what').replaceChildren(`It began to stand out at ${hms(e.start)}: `, ...e.what.slice(0, 2).flatMap((w, i) => [i ? ' and ' : '', templateEl(w.text)]),
    '. Once written down, its first two minutes are saved as its fingerprint.');
  $('label-form').hidden = false;
}

$('label-form').addEventListener('submit', (e) => {
  e.preventDefault();
  if (formEpisode == null) return;
  labelId++;
  $('label-save').disabled = true;
  $('label-status').textContent = 'Saving…';
  worker.postMessage({ type: 'label', id: labelId, episode: formEpisode, title: $('label-title').value, fix: $('label-fix').value });
});

function onLabel(r) {
  if (r.error) {
    $('label-status').replaceChildren(h('span', { class: 'bad', text: r.error }));
    $('label-save').disabled = false;
    return;
  }
  $('label-status').textContent = r.pending ? `Saved. Its fingerprint is taken at ${hms(r.due)}, once two minutes of it are in the file.` : `Written down as #${r.id}.`;
  holdForm = performance.now() + 4000;
  setTimeout(() => { holdForm = 0; if (last) drawForm(last); }, 4000);
}

function drawIncidents(list) {
  incidentsById.clear();
  for (const x of list) incidentsById.set(x.id, x);
  $('c-incidents').textContent = String(list.length);
  $('incidents').tBodies[0].replaceChildren(...list.map((x) => h('tr', { class: x.started >= TODAY_START ? 'fresh' : null },
    h('td', { class: 'num', text: String(x.id) }), h('td', { class: 'nowrap', text: when(x.started) }), h('td', { text: x.title }), h('td', { text: x.fix }),
    h('td', { text: x.source === 'you' ? 'you' : x.source }), h('td', { class: 'num', text: String(x.keys) }))));
}

function onSnapshot(m) {
  last = m;
  const t = m.t;
  $('bar').style.width = `${(100 * t) / m.duration}%`;
  $('clock').textContent = `${hms(t)} UTC${m.slow && state === 'playing' ? ' · slow motion' : ''}`;
  $('c-lines').textContent = fmt.int(m.lines);
  $('c-templates').textContent = fmt.int(m.templates);
  $('c-checks').textContent = fmt.int(m.checks);
  if (m.checks > 1) $('c-check-ms').textContent = fmt.time(m.checkMs / m.checks);
  if (m.engineMs > 50) $('c-rate').textContent = fmt.int(m.lines / (m.engineMs / 1000));
  if (m.incidents) drawIncidents(m.incidents);
  drawNow(m.current);
  drawForm(m);
  for (const c of m.timeline) checks.push(c);
  timeline.set(m.truth, t, m.episodes);
  $('tail').replaceChildren(...(m.tail.length ? m.tail.map(lineEl) : [h('p', { class: 'muted', text: 'None yet.' })]));
  for (const n of m.news) log(n.t, n.text, n.kind);
  if (m.done && started && !$('log').querySelector('[data-t="7200"]')) log(m.duration, '14:00: two hours done. The results are below.', 'teal');
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const perSec = r.lines / (r.engineMs / 1000);
  $('c-rate').textContent = fmt.int(perSec);
  const known = r.outcomes.filter((o) => o.knownAtStart), fresh = r.outcomes.filter((o) => !o.knownAtStart);
  const named = known.filter((o) => o.ok), flagged = fresh.filter((o) => o.ok);
  const delays = named.map((o) => o.named);
  const wrong = r.outcomes.filter((o) => o.wrong).length;
  const countsOk = r.counts.same === r.counts.cells;
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.lines)} lines in two hours, on top of ${fmt.int(r.historyLines)} from the day before. ` }),
    `${r.outcomes.length} incident${r.outcomes.length === 1 ? '' : 's'}. `,
    known.length ? `${named.length} of ${known.length} of a kind in the file named right, ${delays.length ? `${Math.min(...delays)} to ${Math.max(...delays)} seconds after they began` : ''}. ` : '',
    fresh.length ? `${flagged.length} of ${fresh.length} of a kind not in the file flagged as new. ` : '',
    wrong ? `${wrong} named wrong at some point. ` : 'None named wrong. ',
    `${r.falseAlarms.length === 0 ? 'No false alarms' : `${r.falseAlarms.length} false alarm${r.falseAlarms.length === 1 ? '' : 's'}`} in ${fmt.int(r.checks)} checks. `,
    countsOk ? `Every one of ${fmt.int(r.counts.cells)} counts by minute and hour equals a recount of the lines. ` : 'Some counts differ from the recount. ',
    `A check read the file in ${fmt.time(r.checkMs / r.checks)} on average and ${fmt.time(r.maxCheckMs)} at most on this device.`);
  const said = (o) => {
    if (o.knownAtStart) {
      if (o.named == null) return 'Never named';
      const x = incidentsById.get(o.namedAs);
      return `Looks like #${o.namedAs}${x ? `, ${x.title}` : ''} (after ${o.named} s)`;
    }
    return o.newAt == null ? 'Never flagged as new' : `Like nothing in the file (after ${o.newAt} s)`;
  };
  $('outcomes').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Began' }), h('th', { text: 'What happened' }), h('th', { class: 'num', text: 'Stood out after' }), h('th', { text: 'What the file said' }), h('th', { text: 'Right?' }))),
    h('tbody', {}, r.outcomes.map((o) => h('tr', {},
      h('td', { class: 'nowrap', text: hms(o.at) }), h('td', { text: `${o.name}${o.by === 'you' ? ', started by you' : ''}` }),
      h('td', { class: 'num', text: o.detected == null ? 'never' : `${o.detected} s` }),
      h('td', { text: said(o) + (o.wrong ? `; named wrong ${o.wrong} time${o.wrong === 1 ? '' : 's'}` : '') }),
      h('td', {}, h('span', { class: o.ok ? 'ok' : 'bad', text: o.ok ? 'Yes' : 'No' }))))));
  $('outcomes-note').textContent = `A check runs every ${CHECK_EVERY} seconds, after the Engine writes the file, so a time here is at most ${CHECK_EVERY} seconds late. ` +
    `Looks like means a similarity of ${MATCH} or more; partly like, ${PARTLY} or more. ` +
    (r.falseAlarms.length ? `False alarms at ${r.falseAlarms.map((e) => hms(e.start)).join(', ')}.` : 'Something stood out only while an incident was on or fading.');
  const c = r.counts;
  const tick = (ok) => h('span', { class: ok ? 'ok' : 'bad', text: ok ? '✓ ' : '✗ ' });
  $('counts-check').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'What was checked' }), h('th', { class: 'num', text: 'Equal' }))),
    h('tbody', {},
      h('tr', {}, h('td', { text: `Lines a minute, for each service and level, in ${c.minute.windows} minutes` }), h('td', { class: 'num' }, tick(c.minute.same === c.minute.cells), `${fmt.int(c.minute.same)} of ${fmt.int(c.minute.cells)}`)),
      h('tr', {}, h('td', { text: `Lines an hour, for each service and level, in ${c.hour.windows} hours` }), h('td', { class: 'num' }, tick(c.hour.same === c.hour.cells), `${fmt.int(c.hour.same)} of ${fmt.int(c.hour.cells)}`))));
  $('counts-note').textContent = (c.first ? `First difference: ${c.first}. ` : '') +
    `The checks read the file ${fmt.int(r.checks)} times, in ${fmt.time(r.checkMs / r.checks)} on average and ${fmt.time(r.maxCheckMs)} at most. ` +
    `Yesterday's file was made from its 24 hours of lines by the same code (tools/build-dejavu-history.mjs), and building it again gives the same bytes.`;
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table or index' }), h('th', { class: 'num', text: 'Rows' }), h('th', { class: 'num', text: 'Size' }))),
    h('tbody', {}, r.tables.filter((x) => x.rows !== 0 || x.bytes > 8192).map((x) => h('tr', {}, h('td', {}, h('code', { text: x.name })), h('td', { class: 'num', text: x.rows == null ? '' : fmt.int(x.rows) }), h('td', { class: 'num', text: fmt.bytes(x.bytes) })))));
  $('file-note').textContent = `${fmt.bytes(r.fileBytes)} in all. lines_win holds the minute and hour counts of every template since yesterday noon; lines_raw holds the last ten minutes of lines. incidents and incident_keys are the comparison's own tables.`;
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
      buildPresets();
      setState('idle');
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'label': if (m.id === labelId) onLabel(m.result); break;
    case 'start': if (!m.ok) $('hint').textContent = 'The two hours are over. Press Play again to start one.'; break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'export': {
      const url = URL.createObjectURL(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }));
      const a = h('a', { href: url, download: 'shop-dejavu.sqlite' });
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

// The policy, the comparison and the compiled SQL
function showCode(which) {
  for (const id of ['policy', 'dejavu', 'sql']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  if (!files) return;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'dejavu') highlight($('code'), files.dejavuSql, 'sql');
  if (which === 'sql') highlight($('code'), files.schema + '\n' + files.distill, 'sql');
}
for (const id of ['policy', 'dejavu', 'sql']) $(`tab-${id}`).addEventListener('click', () => showCode(id));

// Ask the file
function buildPresets() {
  const presets = [
    ['What stands out now', 'SELECT f.service, f.level, t.template, round(f.now_rate, 1) AS now_a_minute,\n  round(f.usual_rate, 1) AS usual, round(f.score, 2) AS score\nFROM fingerprint_now f JOIN _precomputing_templates t ON t.id = f.template\nORDER BY f.score DESC'],
    ['Which incident it looks like', "SELECT id, title, round(similarity, 3) AS similarity, shared, keys,\n  datetime(started, 'unixepoch') AS began_utc, fix\nFROM deja_vu"],
    ['Every fingerprint in the file', 'SELECT k.incident, i.title, t.template, round(k.now_rate, 1) AS then_a_minute,\n  round(k.usual_rate, 1) AS usual, round(k.score, 2) AS score, k.example\nFROM incident_keys k JOIN incidents i ON i.id = k.incident\nJOIN _precomputing_templates t ON t.id = k.template\nORDER BY k.incident, k.score DESC'],
    ['Failed charges, minute by minute', "SELECT datetime(l.w, 'unixepoch') AS minute_utc, sum(l.n) AS lines\nFROM lines_win l JOIN _precomputing_templates t ON t.id = l.template\nWHERE l.res = 60 AND t.template LIKE 'charge failed%'\nGROUP BY l.w ORDER BY l.w DESC LIMIT 60"],
    ['Errors an hour since yesterday', "SELECT datetime(w, 'unixepoch') AS hour_utc, service, sum(n) AS errors\nFROM lines_win WHERE res = 3600 AND level = 'ERROR'\nGROUP BY w, service ORDER BY w DESC, errors DESC"],
    ['Templates, most lines first', 'SELECT id, service, level, n AS lines, template\nFROM _precomputing_templates ORDER BY n DESC'],
    ['The last ten minutes of warnings', "SELECT datetime(ts, 'unixepoch') AS utc, line FROM lines_raw\nWHERE level IN ('WARN', 'ERROR') ORDER BY ts DESC, rowid DESC LIMIT 40"],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[1][1];
}
function runSql() {
  if ($('sql-run').disabled) { $('sql-status').textContent = 'Pause the run to ask the file.'; return; }
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

setState('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
