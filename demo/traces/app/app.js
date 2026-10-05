// Demo 8 page: the bytes as sent and as kept, spend against budgets, where the input comes from,
// the costliest runs, a call rebuilt from the file, the results and the SQL box. The store of agent
// calls and the Engine run in worker.js.
import { $, h, fmt, renderResult, highlight, isEmbedded } from '../../lib/kit.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });

let state = 'loading';
let files = null;
let last = null;
let started = false;
let queryId = 0, rebuildId = 0;
let finishedCount = -1;
let pickedRun = null;
let lastRebuild = null;
const logged = new Set();

const hms = (ts) => new Date(ts * 1000).toISOString().slice(11, 19);
const hm = (ts) => hms(ts).slice(0, 5);
const money = (nano) => { const v = nano / 1e9; return `$${v >= 0.1 || v === 0 ? v.toFixed(2) : v.toFixed(4)}`; };
const tokens = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e4 ? `${Math.round(n / 1e3)}k` : fmt.int(n));
const sourceName = (s) => (s.startsWith('tool:') ? `${s.slice(5)} output` : { tools: 'tool list', system: 'system prompt', task: 'task', assistant: "agent's messages", user: 'user messages', output: 'replies (output)' }[s] || s);
const SOURCE_COLORS = { system: '#1F3A5F', tools: '#8A95A3', task: '#B8700F', assistant: '#2A4B75', user: '#A0522D' };
const TOOL_COLORS = ['#0E7C86', '#3A86C8', '#7B5EA7', '#2E7D4F', '#C0392B'];
const toolColor = new Map();
const colorOf = (s) => SOURCE_COLORS[s] || toolColor.get(s) || (toolColor.set(s, TOOL_COLORS[toolColor.size % TOOL_COLORS.length]), toolColor.get(s));

function setState(st) {
  state = st;
  const busy = st === 'loading' || st === 'finishing';
  const live = st === 'playing' || st === 'paused';
  $('btn-play').disabled = busy;
  $('play-label').textContent = st === 'playing' ? 'Pause' : st === 'paused' ? 'Resume' : st === 'done' ? 'Play again' : 'Play';
  $('play-icon').setAttribute('d', st === 'playing' ? 'M7 5h3.5v14H7zM13.5 5H17v14h-3.5z' : 'M7 4.5v15l13-7.5z');
  $('btn-stop').disabled = busy || st === 'idle';
  $('btn-review').disabled = busy || st === 'done';
  $('btn-again').disabled = !live || !(last?.finished?.length);
  $('btn-raise').disabled = !live || !raisable();
  $('sql-run').disabled = !(st === 'paused' || st === 'done' || st === 'idle');
  $('sql-status').textContent = st === 'playing' ? 'Pause the day to ask the file.' : '';
  const hints = {
    idle: 'Press Play. The day passes in about a minute and a half, slowing down when a run gets stuck. Report a run again, or raise a budget, as it runs.',
    playing: 'Watch the calls as sent pull away from the file. Report a finished run again to see the meter refuse it, or rebuild a call below.',
    paused: 'Paused. You can ask the file now, rebuild a call, or resume.',
    finishing: 'Finishing the day at full speed, then rebuilding every call and recounting every cost.',
    done: 'Done. The results and the file are below.',
  };
  $('hint').textContent = hints[st] || '';
  if (live && raisable()) $('hint').textContent = `${raisable()} has spent its budget for the day. Raise it, or leave the alert standing.`;
}

const raisable = () => (last?.alerts || []).map((a) => a.repo).find((r) => !last.raised.includes(r)) || null;

$('btn-play').addEventListener('click', () => {
  if (state === 'playing') { worker.postMessage({ type: 'pause' }); setState('paused'); return; }
  if (state === 'done') resetView();
  if (!started) startLog();
  worker.postMessage({ type: 'play' });
  setState('playing');
});
$('btn-stop').addEventListener('click', () => { worker.postMessage({ type: 'stop' }); resetView(); setState('idle'); });
$('btn-review').addEventListener('click', () => {
  if (!started) startLog();
  worker.postMessage({ type: 'review' });
  setState('finishing');
  $('results').hidden = false;
  $('finishing').hidden = false;
  $('results-body').hidden = true;
});
$('btn-again').addEventListener('click', () => worker.postMessage({ type: 'again' }));
$('btn-raise').addEventListener('click', () => { const r = raisable(); if (r) worker.postMessage({ type: 'raise', repo: r }); });

function startLog() {
  started = true;
  const n = files.runs.length, calls = files.runs.reduce((a, r) => a + r.calls, 0), repos = new Set(files.runs.map((r) => r.repo)).size;
  log(last?.open ?? 0, `07:00: the day starts. ${fmt.int(n)} agent runs on ${repos} repositories will make ${fmt.int(calls)} model calls, each reported to the store as its reply comes back.`, 'teal');
}

function resetView() {
  logged.clear();
  started = false;
  last = null;
  finishedCount = -1;
  pickedRun = null;
  $('log').replaceChildren();
  $('results').hidden = true;
  $('sql-out').replaceChildren();
  $('rb-out').replaceChildren();
  $('rb-status').textContent = '';
}

function log(t, text, kind = '') {
  const li = h('li', { 'data-t': t }, h('span', { class: 'when', text: hm(t) }), h('span', { class: `dot ${kind}` }), h('span', { text }));
  const after = [...$('log').children].find((x) => Number(x.dataset.t) <= t);
  $('log').insertBefore(li, after || null);
}

function cbar(label, value, max, cls, text) {
  return h('div', { class: `cbar ${cls}` }, h('span', { class: 'lab', text: label }),
    h('span', { class: 'track' }, h('i', { style: `width:${Math.max(0.3, (100 * value) / Math.max(1, max))}%` })), h('span', { class: 'v', text }));
}

function drawBytes(m) {
  const kept = m.traceBytes ?? last?.traceBytes ?? 0;
  const max = Math.max(m.requestBytes, kept, 1);
  $('bytes').replaceChildren(
    cbar('As sent', m.requestBytes, max, 'raw', fmt.bytes(m.requestBytes)),
    cbar('In the file', kept, max, '', kept ? `${fmt.bytes(kept)} · ${fmt.times(m.requestBytes / kept)} smaller` : '–'));
  $('bytes-note').textContent = m.calls
    ? `${fmt.int(m.pieces)} pieces kept for ${fmt.int(m.calls)} calls. The file is the trace tables with their indexes; with the meter's tables beside them it comes to ${fmt.bytes(m.fileBytes ?? last?.fileBytes)}.`
    : 'Nothing yet. Press Play.';
}

function drawBudgets(m) {
  const top = m.repos.slice(0, 10);
  $('budgets').replaceChildren(...(top.length ? top.map((r) => h('div', { class: `brow${r.reached ? ' over' : ''}` },
    h('span', { class: 'k', text: r.repo, title: r.repo }),
    h('span', { class: 'b' }, h('i', { style: `width:${Math.min(100, (100 * r.cost) / r.lim)}%` })),
    h('span', { class: 'v', text: `${money(r.cost)} of ${money(r.lim)}` })))
    : [h('p', { class: 'muted', text: 'No calls yet.' })]));
  const raised = m.raised.length ? ` You raised ${m.raised.join(', ')} to $1.00.` : '';
  $('budgets-note').textContent = `The ${Math.min(10, m.repos.length)} repositories that have spent the most of ${m.repos.length} so far. Each has $0.20 for the day, one row in repo_budget_limit.${raised} Prices are examples: $0.40 a million fresh input tokens, $0.04 cached, $1.60 output.`;
}

function drawSources(m) {
  const max = Math.max(1, ...m.sources.map((s) => s.cost));
  $('sources').replaceChildren(...(m.sources.length ? m.sources.map((s) => h('div', { class: 'barrow' },
    h('span', { class: 'k', text: sourceName(s.source), title: s.source }),
    h('span', { class: 'b' }, h('i', { style: `width:${(100 * s.cost) / max}%;background:${colorOf(s.source)}` })),
    h('span', { class: 'v', text: s.source === 'output' ? money(s.cost) : `${money(s.cost)} · ${tokens(s.tokens)}` })))
    : [h('p', { class: 'muted', text: 'No calls yet.' })]));
}

function drawRuns(m) {
  $('runs').tBodies[0].replaceChildren(...(m.runs.length ? m.runs.map((r) => h('tr', { class: r.running ? 'fresh' : null, title: r.task },
    h('td', { class: 'runcell' }, h('code', { text: r.id }), h('span', { class: 'repo', text: r.repo })),
    h('td', { class: 'num', text: r.running ? `${r.calls} so far` : fmt.int(r.calls) }), h('td', { class: 'num strong', text: money(r.cost) }),
    h('td', { class: 'num', text: tokens(r.input) }), h('td', { class: 'num', text: r.input ? `${Math.round((100 * r.cached) / r.input)}%` : '–' }),
    h('td', { class: 'num', text: tokens(r.output) })))
    : [h('tr', {}, h('td', { colspan: 6, class: 'muted', text: 'No calls yet.' }))]));
}

function drawRebuildPicker(m) {
  if (m.finished.length === finishedCount) return;
  finishedCount = m.finished.length;
  const sel = $('rb-run');
  if (!m.finished.length) {
    sel.replaceChildren(h('option', { text: 'No run has finished yet' }));
    for (const id of ['rb-run', 'rb-seq', 'rb-go']) $(id).disabled = true;
    return;
  }
  // The longest run first, then the latest.
  const longest = m.finished.reduce((a, r) => (r.calls > a.calls ? r : a), m.finished[0]);
  const list = [longest, ...m.finished.filter((r) => r !== longest)];
  const keep = pickedRun && list.some((r) => r.id === pickedRun) ? pickedRun : longest.id;
  sel.replaceChildren(...list.map((r) => h('option', { value: r.id, text: `${r.id} · ${r.repo} · ${r.calls} calls`, selected: r.id === keep })));
  for (const id of ['rb-run', 'rb-seq', 'rb-go']) $(id).disabled = false;
  syncSeq(!pickedRun);
}

function syncSeq(toLast) {
  const r = last?.finished?.find((x) => x.id === $('rb-run').value);
  if (!r) return;
  $('rb-seq').max = String(r.calls);
  if (toLast || Number($('rb-seq').value) > r.calls) $('rb-seq').value = String(r.calls);
}
$('rb-run').addEventListener('change', () => { pickedRun = $('rb-run').value; syncSeq(true); });
$('rb-go').addEventListener('click', () => {
  const run = $('rb-run').value, seq = Math.max(1, Math.floor(Number($('rb-seq').value) || 1));
  rebuildId++;
  $('rb-status').textContent = 'Reading the file…';
  worker.postMessage({ type: 'rebuild', id: rebuildId, callId: `${run}#${seq}` });
});

function showRebuild(r) {
  lastRebuild = r;
  const out = $('rb-out');
  if (r.error) { $('rb-status').textContent = ''; out.replaceChildren(h('p', { class: 'bad', text: r.error })); return; }
  $('rb-status').textContent = `Read in ${fmt.time(r.ms)}.`;
  const total = r.parts.reduce((a, p) => a + p.bytes, 0);
  const cost = (r.input - r.cached) * 400 + r.cached * 40 + r.output * 1600;
  const head = r.request.slice(0, 1400), tail = r.request.length > 2400 ? r.request.slice(-700) : '';
  out.replaceChildren(
    h('p', {}, h('strong', { text: `Call ${r.seq} of ${r.calls} in ${r.run}` }), `, ${r.repo}, reply at ${hms(r.ts)} UTC. Rebuilt from ${fmt.int(r.pieces)} pieces: ${fmt.int(r.bytes)} bytes, read in ${fmt.time(r.ms)}.`),
    h('ul', { class: 'checks' },
      h('li', { class: r.same ? 'ok' : 'bad' }, h('span', { class: 'mark', text: r.same ? '✓' : '✗' }),
        r.same ? `Identical to the request as the agent sent it, byte for byte: ${fmt.int(r.sentBytes)} bytes each.` : 'Differs from the request as the agent sent it.'),
      h('li', { class: r.shaFile === r.stored ? 'ok' : 'bad' }, h('span', { class: 'mark', text: r.shaFile === r.stored ? '✓' : '✗' }),
        `The same SHA-256 as the one written down in trace_calls when the call was kept.`)),
    h('div', { class: 'table-wrap' }, h('table', { class: 'hashes' }, h('tbody', {},
      [['As the agent sent it, masked', r.shaSent], ['Rebuilt from the file', r.shaFile], ['Written down when kept', r.stored]].map(([k, v]) => h('tr', {}, h('td', { text: k }), h('td', {}, h('code', { class: 'mono', text: v }))))))),
    h('div', { class: 'stack', role: 'img', 'aria-label': 'The request by source' }, ...r.parts.map((p) => h('i', { style: `width:${(100 * p.bytes) / total}%;background:${colorOf(p.source)}`, title: `${sourceName(p.source)}: ${fmt.bytes(p.bytes)}` }))),
    h('div', { class: 'legend' }, ...r.parts.map((p) => h('span', {}, h('i', { class: 'box', style: `background:${colorOf(p.source)}` }), `${sourceName(p.source)} ${fmt.bytes(p.bytes)}${p.n > 1 ? ` (${p.n} pieces)` : ''}`))),
    h('p', { class: 'note', text: `Input ${fmt.int(r.input)} tokens, ${r.input ? Math.round((100 * r.cached) / r.input) : 0}% of them cached; output ${fmt.int(r.output)} tokens; cost ${money(cost)} at the example prices. The pieces are the messages before the reply and the tool list; every earlier call of the run sent most of them already.` }),
    h('pre', { class: 'code request', text: tail ? `${head}\n\n… ${fmt.int(r.request.length - head.length - tail.length)} more characters …\n\n${tail}` : r.request }),
    h('div', { class: 'row' }, h('button', { class: 'btn small', type: 'button', text: 'Download this request', onclick: downloadRequest }), h('span', { class: 'muted', text: 'As JSON, the way the agent sent it, with secrets masked.' })));
}

function downloadRequest() {
  if (!lastRebuild) return;
  save(new Blob([lastRebuild.request], { type: 'application/json' }), `${lastRebuild.callId.replace('#', '-call-')}.json`);
}

function save(blob, name) {
  const url = URL.createObjectURL(blob);
  const a = h('a', { href: url, download: name });
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

function onSnapshot(m) {
  last = m;
  const span = m.end - m.open;
  $('bar').style.width = `${Math.min(100, (100 * (m.t - m.open)) / span)}%`;
  $('clock').textContent = `${hm(m.t)} UTC${m.slow && state === 'playing' ? ' · slow motion' : ''}`;
  $('c-calls').textContent = fmt.int(m.calls);
  $('c-sent').textContent = fmt.bytes(m.requestBytes);
  if (m.traceBytes != null) $('c-kept').textContent = fmt.bytes(m.calls ? m.traceBytes : 0);
  $('c-cost').textContent = `$${(m.cost / 1e9).toFixed(2)}`;
  $('c-masked').textContent = fmt.int(m.masked);
  $('c-repeat').textContent = fmt.int(m.repeats);
  drawBytes(m);
  drawBudgets(m);
  drawSources(m);
  drawRuns(m);
  drawRebuildPicker(m);
  for (const n of m.news) log(n.t, n.text, n.kind);
  if (m.done && !logged.has('done')) { logged.add('done'); log(m.end, `${hm(m.end)}: the day is done. The results are below.`, 'teal'); }
  setState(state);
}

function onResults(r) {
  setState('done');
  $('finishing').hidden = true;
  $('results-body').hidden = false;
  const rate = r.calls / (r.engineMs / 1000);
  const allEqual = r.checks.every((c) => c.equal === c.n && !c.extra);
  $('results-summary').replaceChildren(
    h('strong', { text: `${fmt.int(r.calls)} model calls from ${fmt.int(r.runs)} agent runs on ${r.repos} repositories. ` },),
    `Sent whole, they come to ${fmt.bytes(r.requestBytes)}; the store keeps them in ${fmt.bytes(r.traceBytes)}, ${fmt.times(r.requestBytes / r.traceBytes)} smaller, `,
    r.same === r.rebuilt ? `and every one of them rebuilt from the file is the request as sent, byte for byte. ` : `but ${r.rebuilt - r.same} rebuilt calls differ from the requests as sent. `,
    `${r.masked} secrets were masked before storage and ${r.left.length ? `${r.left.length} are` : 'none is'} left in the file. `,
    allEqual ? `Every cost in the meter matches a recount of every call to the billionth of a dollar: ${money(r.total)} for the day at the example prices. ` : 'Some costs differ from the recount. ',
    `On this device the store and the Engine kept ${fmt.int(rate)} calls a second, with a checkpoint every minute of the day.`);
  const li = (ok, text) => h('li', { class: ok ? 'ok' : 'bad' }, h('span', { class: 'mark', text: ok ? '✓' : '✗' }), text);
  const items = [
    li(r.same === r.rebuilt && r.rebuilt === r.calls, `${fmt.int(r.same)} of ${fmt.int(r.calls)} calls, rebuilt from the file with SQL alone, are the requests as the agent sent them, byte for byte: ${fmt.bytes(r.requestBytes)} compared.`),
    li(r.masked === r.planted && !r.left.length, `${r.masked} secrets masked by the store, the same ${r.planted} the page finds in the runs with the same patterns; ${r.left.length} left in the file, with every piece searched.`),
    ...r.checks.map((c) => li(c.equal === c.n && !c.extra, `${c.name}: ${fmt.int(c.equal)} of ${fmt.int(c.n)} equal to the recount${c.extra ? `, ${c.extra} extra in the file` : ''}.`)),
    li(r.byCallsStream === r.total && r.bySourceStream === r.total, `The two streams agree: cost by call and cost by source both come to ${money(r.total)}, and so does the recount.`),
  ];
  if (r.again.length) {
    const calls = r.again.reduce((a, x) => a + x.calls, 0);
    items.push(li(r.refused.calls === calls, `${fmt.int(calls)} calls reported a second time (${r.again.map((x) => x.run).join(', ')}), all refused as repeats: ${fmt.int(r.refused.calls)} in calls_refused and ${fmt.int(r.refused.context)} source events in context_refused. The costs above count each call once.`));
  }
  $('checks').replaceChildren(...items);
  $('checks-note').textContent = 'The recount works out each call from the runs: its input is the tool list and every message before the reply, and the provider serves the previous call\'s whole input from its cache when that call was sent less than five minutes before. '
    + `Rebuilding all ${fmt.int(r.rebuilt)} calls and comparing them took ${fmt.time(r.rebuildMs)}.`;
  const out = $('alert-out');
  if (r.alerts.length) {
    out.replaceChildren(...r.alerts.map((a) => h('p', {},
      h('strong', { text: `${a.repo} spent its ${money(a.lim)} for the day at ${hms(a.ts)}, ` }),
      `in call ${a.callId.split('#')[1]} of run ${a.run}. The run made ${fmt.int(a.afterRun)} more calls after that`,
      a.after > a.afterRun ? `, and the repository ${fmt.int(a.after - a.afterRun)} more in later runs: ` : ': ',
      `${money(a.afterCost)} spent past the budget. `,
      `A gateway that reads repo_budget before each call would have held them back, and paid for one read of a view each time.`,
      last?.raised?.includes(a.repo) ? ' You raised its budget to $1.00 during the day.' : '')));
  } else out.replaceChildren(h('p', { class: 'muted', text: 'No repository spent its budget.' }));
  const big = r.tables.filter((x) => x.bytes >= 32768), small = r.tables.filter((x) => x.bytes < 32768);
  $('file-tables').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Table or index' }), h('th', { class: 'num', text: 'Rows' }), h('th', { class: 'num', text: 'Size' }))),
    h('tbody', {}, big.map((x) => h('tr', {}, h('td', {}, h('code', { text: x.name })), h('td', { class: 'num', text: x.rows == null ? '' : fmt.int(x.rows) }), h('td', { class: 'num', text: fmt.bytes(x.bytes) }))),
      small.length ? h('tr', {}, h('td', { class: 'muted', text: `${small.length} smaller tables and indexes` }), h('td', {}), h('td', { class: 'num', text: fmt.bytes(small.reduce((a, x) => a + x.bytes, 0)) })) : null));
  $('file-note').textContent = `trace_pieces holds each message and tool list once, trace_calls each call as the list of its pieces with its size and SHA-256, and the view trace_requests rebuilds any call. The meter keeps every call and every source event whole for 30 days after the day closes (calls_raw, context_raw), and hourly and daily windows after that. The whole file: ${fmt.bytes(r.fileBytes)}.`;
  $('download-row').hidden = host === 'artifact';
}

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'ready':
      files = m;
      $('sqlite-version').textContent = m.sqlite;
      $('pc-version').textContent = m.version;
      $('data-credit').textContent = m.meta.credit || `The runs are generated: ${m.meta.label.replace(/^Agent runs generated by /, 'made by ')}.`;
      showCode('policy');
      buildPresets();
      setState('idle');
      break;
    case 'snapshot': onSnapshot(m); break;
    case 'finishing': setState('finishing'); $('results').hidden = false; $('finishing').hidden = false; $('results-body').hidden = true; break;
    case 'results': onResults(m); break;
    case 'rebuild': if (m.id === rebuildId) showRebuild(m.result); break;
    case 'query': if (m.id === queryId) renderResult($('sql-out'), m.result); break;
    case 'export': save(new Blob([m.bytes], { type: 'application/vnd.sqlite3' }), 'agent-traces.sqlite'); break;
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

// The policy, the compiled SQL, the store's tables and the secret patterns
function showCode(which) {
  for (const id of ['policy', 'sql', 'store', 'secrets']) $(`tab-${id}`).setAttribute('aria-selected', String(id === which));
  if (!files) return;
  if (which === 'policy') highlight($('code'), files.policy, 'policy');
  if (which === 'sql') highlight($('code'), files.schema, 'sql');
  if (which === 'store') highlight($('code'), `-- Made by the store of agent calls (traces/store.go), beside the policy's tables.\n\n${files.traceSchema}`, 'sql');
  if (which === 'secrets') {
    $('code').textContent = '# Masked in every message and tool list before it is stored. What matches becomes the label;\n'
      + '# where the pattern keeps its first group, such as the name of a variable, the label follows it.\n\n'
      + files.patterns.map((p) => `${p.label}${p.keep ? '  (keeps group 1)' : ''}\n  ${p.re}`).join('\n\n');
  }
}
for (const id of ['policy', 'sql', 'store', 'secrets']) $(`tab-${id}`).addEventListener('click', () => showCode(id));

// Ask the file
function buildPresets() {
  const stuck = files.runs.reduce((a, r) => (r.calls > a.calls ? r : a), files.runs[0]);
  const presets = [
    ['Spend against budgets', 'SELECT repo, round(used / 1e9, 4) AS usd, round(lim / 1e9, 2) AS budget, reached\nFROM repo_budget ORDER BY used DESC'],
    ['Rebuild a call', `SELECT call_id, length(request) AS bytes, substr(request, 1, 400) AS starts\nFROM trace_requests WHERE call_id = '${stuck.id}#40'`],
    ['Cost by source', 'SELECT source, round(value / 1e9, 4) AS usd FROM source_cost_day ORDER BY value DESC'],
    ['The stuck run, call by call', `SELECT seq, time(ts, 'unixepoch') AS utc, input_tokens, cached_tokens,\n  output_tokens, request_bytes\nFROM trace_calls WHERE run = '${stuck.id}' ORDER BY seq`],
    ['Pieces sent most often', 'SELECT p.id, p.source, p.bytes, count(*) AS calls, p.bytes * count(*) AS bytes_as_sent\nFROM trace_calls c, json_each(c.messages) j JOIN trace_pieces p ON p.id = j.value\nGROUP BY p.id ORDER BY bytes_as_sent DESC LIMIT 10'],
    ['Secrets masked', "SELECT id, source, substr(body, max(1, instr(body, '[redacted') - 80), 160) AS around\nFROM trace_pieces WHERE body LIKE '%[redacted%'"],
    ['Spend by the hour', "SELECT time(w, 'unixepoch') AS hour, sum(n) AS calls, round(sum(cost_nano_sum) / 1e9, 4) AS usd\nFROM calls_win WHERE res = 3600 GROUP BY w ORDER BY w"],
    ['Refused as repeats', 'SELECT reason, repo, run, n FROM calls_refused'],
    ['Bytes per table', 'SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC'],
    ['What the file says about itself', 'SELECT name, kind, stream, detail FROM _precomputing_objects'],
  ];
  $('presets').replaceChildren(...presets.map(([name, sql]) => h('button', { type: 'button', text: name, onclick: () => { $('sql-text').value = sql; runSql(); } })));
  $('sql-text').value = presets[0][1];
}
function runSql() {
  if ($('sql-run').disabled) { $('sql-status').textContent = 'Pause the day to ask the file.'; return; }
  queryId++;
  worker.postMessage({ type: 'query', id: queryId, sql: $('sql-text').value });
}
$('sql-run').addEventListener('click', runSql);
$('sql-text').addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runSql(); } });
$('btn-download').addEventListener('click', () => worker.postMessage({ type: 'export' }));

setState('loading');
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
