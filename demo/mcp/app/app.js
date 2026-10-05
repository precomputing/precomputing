// Demo 7 page: the files, the four questions, calls of your own and the recorded session.
// The servers run in worker.js; run.js holds the questions and their checks.
import { $, h, fmt, isEmbedded } from '../../lib/kit.js';
import { SERVERS, QUESTIONS, PROTOCOL } from './run.js';

const host = window.PRECOMPUTING_HOST || 'site';
const worker = new Worker(new URL('worker.js', import.meta.url), { type: 'module' });

let state = 'loading'; // loading, idle, asking, done
let facts = null;
let nextId = 1;
const waiting = new Map();
const described = new Map(); // server -> describe_file call
const answered = new Map();  // question index -> answer
const cards = [];
const PAUSE = 450;           // between an agent's calls, so they can be followed

const ask = (msg, transfer) => new Promise((resolve, reject) => {
  const id = nextId++;
  waiting.set(id, { resolve, reject });
  worker.postMessage({ ...msg, id }, transfer || []);
});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const tok = (n) => `about ${fmt.int(n)} token${n === 1 ? '' : 's'}`;
const serverOf = (name) => SERVERS.find((s) => s.name === name);

// wire writes a call as it travels over HTTP: the request, then the reply.
function wire(c) {
  const head = Object.entries(c.headers).map(([k, v]) => `${k}: ${v}`).join('\n');
  let body = c.body;
  try { body = JSON.stringify(JSON.parse(c.body), null, 2); } catch { /* sent as written */ }
  const reason = { 200: 'OK', 202: 'Accepted', 400: 'Bad Request', 403: 'Forbidden', 404: 'Not Found' }[c.status] || '';
  let resp = c.response || '';
  try { resp = JSON.stringify(JSON.parse(c.response), null, 2); } catch { /* shown as it came */ }
  return `POST /mcp HTTP/1.1\n${head}\n\n${body}\n\nHTTP/1.1 ${c.status} ${reason}${resp ? '\nContent-Type: application/json\n\n' + resp : ''}\n`;
}

// callView shows one tool call: what was asked, the text the model reads, and the wire.
// A file's description is long and read once, so it starts folded.
function callView(c, note, folded) {
  const args = JSON.stringify(c.args);
  const result = h('pre', { class: `code result${c.isError ? ' err' : ''}`, text: c.text });
  return h('li', { class: 'call' },
    h('div', { class: 'callhead' },
      h('code', { class: 'tool', text: c.tool }),
      h('span', { class: 'args', text: args === '{}' ? '' : args }),
      h('span', { class: 'cmeta', text: `${fmt.int(c.bytes)} bytes, ${tok(c.tokens)}, ${fmt.time(c.ms)}` })),
    note ? h('p', { class: 'note', text: note }) : null,
    folded ? h('details', { class: 'wire' }, h('summary', { text: 'What the file keeps' }), result) : result,
    h('details', { class: 'wire' }, h('summary', { text: 'On the wire' }), h('pre', { class: 'code', text: wire(c) })));
}

// Files
function fileRows() {
  const tbody = $('files').tBodies[0];
  tbody.replaceChildren(...SERVERS.map((s) => {
    const f = facts.files.find((x) => x.name === s.name);
    const d = described.get(s.name);
    return h('tr', {},
      h('td', {}, h('code', { text: s.name })),
      h('td', {}, h('a', { href: s.page, text: `Demo ${s.demo}: ${s.title}` }), h('span', { class: 'muted', text: ` (${s.runtime})` })),
      h('td', { text: `${f.when[0].toUpperCase()}${f.when.slice(1)}` }),
      h('td', { class: 'num', title: `${fmt.int(f.fadedBytes)} bytes; ${fmt.int(f.fullBytes)} at the end of the demo's run`, text: fmt.bytes(f.fadedBytes) }),
      h('td', { class: 'num', text: d ? tok(d.tokens) : '–' }),
      h('td', {},
        h('button', { class: 'btn small', type: 'button', disabled: state === 'loading', onclick: () => showDescribe(s.name) }, 'What it keeps'),
        ' ',
        host === 'artifact' ? null : h('button', { class: 'btn small', type: 'button', disabled: state === 'loading', onclick: () => download(s.name) }, 'Download')));
  }));
}

async function describe(server) {
  if (!described.has(server)) described.set(server, await ask({ type: 'describe', server }));
  return described.get(server);
}

async function showDescribe(server) {
  const d = await describe(server);
  const out = $('describe-out');
  out.hidden = false;
  out.textContent = `describe_file on ${server}: ${fmt.int(d.bytes)} bytes, ${tok(d.tokens)}\n\n${d.text}`;
  fileRows();
  counters();
}

async function download(server) {
  const bytes = await ask({ type: 'export', server });
  const url = URL.createObjectURL(new Blob([bytes], { type: 'application/vnd.sqlite3' }));
  const a = h('a', { href: url, download: `${server}.sqlite` });
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

// Question cards
for (const [i, q] of QUESTIONS.entries()) {
  const s = serverOf(q.server);
  const el = {
    calls: h('ol', { class: 'calls' }),
    says: h('p', { class: 'says', hidden: true }),
    compare: h('div', { class: 'compare', hidden: true }),
    checks: h('ul', { class: 'checks', hidden: true }),
    button: h('button', { class: 'btn small primary', type: 'button', disabled: true, onclick: () => askOne(i, true) }, 'Ask'),
  };
  const card = h('section', { class: 'card qcard', id: `q-${q.id}` },
    h('div', { class: 'qhead' },
      h('span', { class: 'qbadge', text: `Demo ${s.demo} · ${s.name}` }),
      h('h2', { text: q.ask }),
      el.button),
    el.calls, el.says, el.compare, el.checks);
  cards.push(el);
  $('questions').append(card);
}

async function askOne(i, alone) {
  const q = QUESTIONS[i];
  const el = cards[i];
  if (answered.has(i)) return;
  el.button.disabled = true;
  el.calls.replaceChildren();
  if (alone) setState('asking');
  const first = !described.has(q.server);
  const d = await describe(q.server);
  el.calls.append(callView(d, first
    ? `The agent reads what the ${q.server} file keeps, once. It learns the stream and key names it needs from this.`
    : `The agent read what the ${q.server} file keeps earlier; it is not read again.`, true));
  el.calls.lastChild.classList.add('described');
  fileRows();
  counters();
  const a = await ask({ type: 'ask', i });
  for (const c of a.calls) {
    await sleep(PAUSE);
    el.calls.append(callView(c));
  }
  answered.set(i, a);
  el.says.hidden = false;
  el.says.replaceChildren(h('strong', { text: 'What the answer says. ' }), a.says);
  const ratio = a.raw.tokens / a.tokens;
  el.compare.hidden = false;
  el.compare.replaceChildren(
    h('div', { class: 'cbar' }, h('span', { class: 'lab', text: 'The answers' }), h('span', { class: 'track' }, h('i', { style: `width:${Math.max(0.4, 100 / ratio)}%` })), h('span', { class: 'v', text: tok(a.tokens) })),
    h('div', { class: 'cbar raw' }, h('span', { class: 'lab', text: 'The raw data' }), h('span', { class: 'track' }, h('i', { style: 'width:100%' })), h('span', { class: 'v', text: tok(a.raw.tokens) })),
    h('p', { class: 'note' }, h('strong', { text: `${fmt.int(Math.round(ratio))} times more. ` }),
      `Reading ${a.raw.what} would take ${fmt.int(a.raw.events)} ${q.id === 'shop' ? 'lines' : q.id === 'trades' ? 'trades' : 'requests'}, ${fmt.bytes(a.raw.bytes)}. `,
      q.id === 'latency' || q.id === 'trades' ? 'The file never kept them beyond five minutes; it counted them as they arrived.' : 'The file kept them for a while; they have faded since.'));
  el.checks.hidden = false;
  el.checks.replaceChildren(...a.checks.map((c) => h('li', { class: c.ok ? 'ok' : 'bad' },
    h('span', { class: 'mark', text: c.ok ? '✓' : '✗' }), `${c.what}: `, h('span', { class: 'mono', text: c.got }),
    c.ok ? '' : h('span', { text: ` (the raw events say ${c.want})` }))));
  counters();
  if (alone) setState(answered.size === QUESTIONS.length ? 'done' : 'idle');
  if (answered.size === QUESTIONS.length) results();
}

async function askAll() {
  setState('asking');
  for (let i = 0; i < QUESTIONS.length; i++) {
    if (answered.has(i)) continue;
    cards[i].button.closest('.card').scrollIntoView?.({ behavior: 'smooth', block: 'start' });
    await askOne(i, false);
    $('bar').style.width = `${(100 * answered.size) / QUESTIONS.length}%`;
    await sleep(PAUSE);
  }
  setState('done');
}

function counters() {
  const all = [...answered.values()];
  const calls = all.reduce((n, a) => n + a.calls.length, 0) + described.size;
  const answerTok = all.reduce((n, a) => n + a.tokens, 0);
  const descTok = [...described.values()].reduce((n, d) => n + d.tokens, 0);
  const rawTok = all.reduce((n, a) => n + a.raw.tokens, 0);
  $('c-calls').textContent = fmt.int(calls);
  $('c-answer').textContent = fmt.int(answerTok);
  $('c-describe').textContent = fmt.int(descTok);
  $('c-raw').textContent = fmt.int(rawTok);
  $('c-ratio').textContent = all.length ? `${fmt.int(Math.round(rawTok / (answerTok + descTok)))}×` : '–';
  const checks = all.flatMap((a) => a.checks);
  $('c-checks').textContent = checks.length ? `${checks.filter((c) => c.ok).length} of ${checks.length}` : '–';
}

function results() {
  const all = QUESTIONS.map((_, i) => answered.get(i));
  const checks = all.flatMap((a) => a.checks.map((c) => ({ ...c, q: a.id })));
  const passed = checks.filter((c) => c.ok).length;
  const answerTok = all.reduce((n, a) => n + a.tokens, 0);
  const descTok = [...described.values()].reduce((n, d) => n + d.tokens, 0);
  const rawTok = all.reduce((n, a) => n + a.raw.tokens, 0);
  const calls = all.reduce((n, a) => n + a.calls.length, 0);
  $('results').hidden = false;
  $('results-summary').replaceChildren(
    h('strong', { text: `Four questions, ${calls} tool calls and ${described.size} file descriptions: ${fmt.int(answerTok + descTok)} tokens in all. ` }),
    `The raw data behind the answers would take about ${fmt.int(rawTok)} tokens, ${fmt.int(Math.round(rawTok / (answerTok + descTok)))} times more, and most of it is no longer in the files. `,
    passed === checks.length ? `All ${checks.length} checks against the raw events pass.` : h('span', { class: 'bad', text: `${checks.length - passed} of ${checks.length} checks fail.` }));
  $('checks').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', { text: 'Question' }), h('th', { text: 'What' }), h('th', { text: 'The answer' }), h('th', { text: 'The raw events' }), h('th', { text: 'Result' }))),
    h('tbody', {}, checks.map((c) => h('tr', {},
      h('td', {}, h('code', { text: c.q })), h('td', { class: 'wrapcell', text: c.what }),
      h('td', { class: 'mono wrapcell', text: c.got }), h('td', { class: 'mono wrapcell', text: c.want }),
      h('td', {}, h('span', { class: c.ok ? 'ok' : 'bad', text: c.ok ? (c.rule === 'equal' ? 'same' : c.rule) : 'differs' }))))));
  $('checks-note').textContent = 'Percentiles come from sketches and are promised within 1%; every other figure must be equal. The same calls sent to the native server, precomputing mcp on the same files, return the same text to the byte: tools/run-demo7.mjs --native compares every answer.';
}

// Calls of your own
const EXAMPLES = {
  describe_file: {},
  get_answer: { latency: { name: 'p99_ms' }, trades: { name: 'day_high', period: '2026-09-28' }, usage: { name: 'monthly_tokens', period: '2026-09' }, shop: { name: 'lines' } },
  get_windows: {
    latency: { stream: 'latency', every: '1h', stats: ['n', 'avg', 'p99'] },
    trades: { stream: 'trades', where: { symbol: 'SIM4' }, from: '2026-09-28T11:00:00-04:00', to: '2026-09-28T11:06:00-04:00', every: '1m', stats: ['n', 'first', 'max', 'min', 'last'] },
    usage: { stream: 'usage', by: ['customer'], every: 'all', stats: ['n', 'sum'] },
    shop: { stream: 'payments_by_provider_result', from: '2026-09-29T12:30:00Z', to: '2026-09-29T13:00:00Z', every: '10m' },
  },
  get_kept: {
    latency: { kind: 'anomalies', stream: 'latency', order: 'unusual', limit: 5 },
    trades: { kind: 'anomalies', stream: 'trades' },
    usage: { kind: 'raw', stream: 'usage', where: { customer: 'harbor' }, limit: 5 },
    shop: { kind: 'samples', stream: 'errors', from: '2026-09-29T12:40:00Z', limit: 5 },
  },
  query: {
    latency: { sql: 'SELECT endpoint, value AS p99_ms FROM p99_ms ORDER BY value DESC' },
    trades: { sql: 'SELECT symbol, value AS last_price FROM last_price' },
    usage: { sql: "SELECT name, printf('%.2f', due_cents / 100.0) AS due_usd FROM invoices WHERE period = '2026-09' ORDER BY due_cents DESC" },
    shop: { sql: 'SELECT service, level, count(*) AS templates, sum(n) AS lines FROM _precomputing_templates GROUP BY 1, 2 ORDER BY lines DESC' },
  },
};
let mode = 'tool';
function example() {
  const server = $('try-server').value, tool = $('try-tool').value;
  const ex = tool === 'describe_file' ? {} : EXAMPLES[tool][server];
  $('try-args').value = JSON.stringify(ex, null, 2);
}
function anyExample() {
  $('any-headers').value = `Content-Type: application/json\nAccept: application/json, text/event-stream\nMCP-Protocol-Version: ${PROTOCOL}\nMcp-Method: tools/call\nMcp-Name: get_answer`;
  $('any-body').value = JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: {
    name: 'get_answer', arguments: { name: 'p99_ms' },
    _meta: { 'io.modelcontextprotocol/protocolVersion': PROTOCOL, 'io.modelcontextprotocol/clientCapabilities': {} } } }, null, 2);
}
function setMode(m) {
  mode = m;
  $('tab-tool').setAttribute('aria-selected', String(m === 'tool'));
  $('tab-any').setAttribute('aria-selected', String(m === 'any'));
  $('pane-tool').hidden = m !== 'tool';
  $('pane-any').hidden = m !== 'any';
}
$('tab-tool').addEventListener('click', () => setMode('tool'));
$('tab-any').addEventListener('click', () => setMode('any'));
$('try-server').addEventListener('change', example);
$('try-tool').addEventListener('change', example);

async function tryRun() {
  if ($('try-run').disabled) return;
  const out = $('try-out');
  let c;
  if (mode === 'tool') {
    let args;
    try { args = JSON.parse($('try-args').value || '{}'); } catch (e) { $('try-status').textContent = `The arguments are not JSON: ${e.message}`; return; }
    c = await ask({ type: 'tool', server: $('try-server').value, tool: $('try-tool').value, args });
  } else {
    const headers = {};
    for (const line of $('any-headers').value.split('\n')) {
      const at = line.indexOf(':');
      if (at > 0) headers[line.slice(0, at).trim()] = line.slice(at + 1).trim();
    }
    c = await ask({ type: 'send', server: $('any-server').value, body: $('any-body').value, headers });
  }
  let status = `HTTP ${c.status}`;
  if (c.reply?.error) status += `: error ${c.reply.error.code}, ${c.reply.error.message}`;
  else if (c.text != null) status += `, ${fmt.int(c.bytes)} bytes of text, ${tok(c.tokens)}${c.isError ? ', a tool error the model can correct' : ''}`;
  else if (c.status === 202) status += ': accepted, no reply';
  $('try-status').textContent = `${status}, ${fmt.time(c.ms)}`;
  const text = c.text ?? (c.reply?.result?.content ? c.reply.result.content.map((x) => x.text).join('') : null);
  out.replaceChildren(
    text != null ? h('pre', { class: `code result${c.isError ? ' err' : ''}`, text }) : null,
    h('details', { class: 'wire', open: text == null }, h('summary', { text: 'On the wire' }), h('pre', { class: 'code', text: wire(c) })));
}
$('try-run').addEventListener('click', tryRun);
for (const id of ['try-args', 'any-body', 'any-headers']) {
  $(id).addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); tryRun(); } });
}

// The recorded session
async function session() {
  try {
    const s = await fetch(new URL('session.json', import.meta.url)).then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.json(); });
    $('session-intro').textContent = s.intro;
    $('session-body').replaceChildren(...s.turns.map((t) => {
      const bytes = t.steps.reduce((n, x) => n + x.bytes, 0);
      return h('div', { class: 'card turn' },
        h('p', { class: 'you' }, h('strong', { text: 'Question. ' }), t.question),
        h('ol', { class: 'calls' }, t.steps.map((x) => h('li', { class: 'call' },
          h('div', { class: 'callhead' }, h('span', { class: 'qbadge', text: x.server }), h('code', { class: 'tool', text: x.tool }),
            h('span', { class: 'args', text: JSON.stringify(x.args) === '{}' ? '' : JSON.stringify(x.args) }),
            h('span', { class: 'cmeta', text: `${fmt.int(x.bytes)} bytes, ${tok(Math.round(x.bytes / 4))}` })),
          x.why ? h('p', { class: 'note', text: x.why }) : null,
          h('details', {}, h('summary', { text: 'The result' }), h('pre', { class: `code result${x.isError ? ' err' : ''}`, text: x.text }))))),
        h('div', { class: 'agent' }, h('strong', { text: "The agent's answer. " }), ...t.answer.split('\n\n').map((p, k) => (k ? h('p', { text: p }) : p))),
        h('p', { class: 'note', text: `${t.steps.length} calls, ${fmt.int(bytes)} bytes of results, ${tok(Math.round(bytes / 4))}.` }));
    }));
  } catch (e) {
    $('session-intro').textContent = `The recording could not be loaded (${e.message}).`;
  }
}

$('connect-code').textContent = `# On the machine with the file: read only, with a token file
# (a line such as "read 3f8c0a1e6d2b47c9a5e1f0b28d6c4a71 agent")
precomputing serve --read-only --token-file tokens usage.sqlite

# In the agent's MCP configuration
{
  "mcpServers": {
    "usage": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer 3f8c0a1e6d2b47c9a5e1f0b28d6c4a71" }
    },
    "shop": {
      "command": "precomputing",
      "args": ["mcp", "shop.sqlite"]
    }
  }
}`;

function setState(s) {
  state = s;
  const busy = s === 'loading' || s === 'asking';
  $('btn-ask').disabled = busy || answered.size === QUESTIONS.length;
  $('btn-reset').disabled = busy || (answered.size === 0 && described.size === 0);
  $('try-run').disabled = s === 'loading';
  for (const [i, el] of cards.entries()) el.button.disabled = busy || answered.has(i);
  const hints = {
    idle: 'Press Ask the four questions. The agent reads each file once, then asks the questions one by one; every call runs here, in the MCP server of the WebAssembly build.',
    asking: 'Asking. Each call shows the text the model reads, its size, and what went over the wire.',
    done: 'Done. The results are below, and you can make calls of your own.',
  };
  if (hints[s]) $('hint').textContent = hints[s];
  if (s !== 'loading') $('status').textContent = s === 'done' ? 'All four answered' : `${answered.size} of ${QUESTIONS.length} answered`;
}

$('btn-ask').addEventListener('click', askAll);
$('btn-reset').addEventListener('click', () => {
  answered.clear();
  described.clear();
  for (const el of cards) { el.calls.replaceChildren(); el.says.hidden = el.compare.hidden = el.checks.hidden = true; }
  $('results').hidden = true;
  $('describe-out').hidden = true;
  $('bar').style.width = '0';
  fileRows();
  counters();
  setState('idle');
});

worker.onmessage = (ev) => {
  const m = ev.data;
  switch (m.type) {
    case 'status': $('status').textContent = m.text; break;
    case 'progress': $('bar').style.width = `${(100 * m.loaded) / m.total}%`; break;
    case 'ready':
      facts = m.facts;
      $('sqlite-version').textContent = m.sqlite;
      $('pc-version').textContent = m.version;
      $('bar').style.width = '0';
      for (const sel of ['try-server', 'any-server']) $(sel).replaceChildren(...SERVERS.map((s) => h('option', { value: s.name, text: `${s.name} (Demo ${s.demo})` })));
      $('try-tool').replaceChildren(...m.tools.map((t) => h('option', { value: t.name, text: t.name })));
      $('try-tool').value = 'get_windows';
      example();
      anyExample();
      fileRows();
      setState('idle');
      break;
    case 'reply': {
      const w = waiting.get(m.id);
      if (!w) break;
      waiting.delete(m.id);
      if (m.error) { $('hint').replaceChildren(h('span', { class: 'bad', text: m.error })); w.reject(new Error(m.error)); } else w.resolve(m.result);
      break;
    }
    case 'error':
      $('hint').replaceChildren(h('span', { class: 'bad', text: m.message }));
      if (state === 'loading') $('status').textContent = 'Could not start';
      break;
  }
};
worker.onerror = (e) => {
  $('hint').replaceChildren(h('span', { class: 'bad', text: `The demo could not start in this browser (${e.message || 'worker error'}). It needs a current Chrome, Edge, Firefox or Safari.` }));
  $('status').textContent = 'Could not start';
};

setState('loading');
session();
if (host === 'site' && isEmbedded()) document.documentElement.classList.add('embedded');
if (host !== 'site') document.documentElement.classList.add(`host-${host}`);
