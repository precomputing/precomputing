// One run of Demo 7: an AI agent's questions to the four demos' files over MCP. Each file gets its
// own MCP server, the Go build of `precomputing serve` compiled to WebAssembly, and every call is
// the JSON-RPC message a 2026-07-28 client sends over HTTP, headers included. Runs the same way in
// the page's worker and in Node (tools/run-demo7.mjs), which is how the published numbers are made.

import { sqliteReader, openReadOnly } from '../../lib/engine.js';

export const PROTOCOL = '2026-07-28';
const META = {
  'io.modelcontextprotocol/protocolVersion': PROTOCOL,
  'io.modelcontextprotocol/clientCapabilities': {},
  'io.modelcontextprotocol/clientInfo': { name: 'precomputing-demo-5', version: '0.1.1' },
};

// The four servers, one per demo file.
export const SERVERS = [
  { name: 'latency', demo: 1, title: 'API traffic', runtime: 'SQL runtime', page: '/demo/sql/' },
  { name: 'trades', demo: 2, title: 'A trading day', runtime: 'Engine', page: '/demo/engine/' },
  { name: 'usage', demo: 3, title: 'A month of AI usage', runtime: 'Meter', page: '/demo/meter/' },
  { name: 'shop', demo: 4, title: "A web shop's logs", runtime: 'Logs', page: '/demo/logs/' },
];

// About one token for every four bytes of text: an estimate, the same for answers and raw data.
export const tokens = (bytes) => Math.round(bytes / 4);
const utf8 = new TextEncoder();

// csv reads the rows under a tool answer's first line.
export function csv(text) {
  const lines = [];
  let cur = [], field = '', q = false;
  const body = text.slice(text.indexOf('\n') + 1);
  for (let i = 0; i < body.length; i++) {
    const c = body[i];
    if (q) {
      if (c === '"' && body[i + 1] === '"') { field += '"'; i++; } else if (c === '"') q = false; else field += c;
    } else if (c === '"') q = true;
    else if (c === ',') { cur.push(field); field = ''; } else if (c === '\n') { cur.push(field); lines.push(cur); cur = []; field = ''; } else field += c;
  }
  if (field || cur.length) { cur.push(field); lines.push(cur); }
  const head = lines[0] || [];
  return lines.slice(1).filter((l) => l.length === head.length).map((l) => Object.fromEntries(head.map((h, i) => [h, l[i]])));
}

const near = (a, b, rel) => Math.abs(a - b) <= rel * Math.abs(b);
const pctApart = (a, b) => (100 * Math.abs(a - b) / Math.abs(b)).toFixed(2);
const usd = (nano) => { const c = (BigInt(nano) + 5000000n) / 10000000n; return `$${Number(c / 100n).toLocaleString('en-US')}.${String(c % 100n).padStart(2, '0')}`; };
const hm = (iso) => iso.slice(11, 16);

// The questions, the calls an agent makes to answer them, and how each answer is checked against
// the facts: separate code's count of the raw events (tools/mcp-files.mjs).
export const QUESTIONS = [
  {
    id: 'latency', server: 'latency',
    ask: 'What was the p99 latency of /api/checkout between 10:00 and 11:00 UTC, and which requests stood out?',
    calls: [
      ['get_windows', { stream: 'latency', where: { endpoint: '/api/checkout' }, from: '2026-09-28T10:00:00Z', to: '2026-09-28T11:00:00Z', every: 'all', stats: ['n', 'avg', 'p50', 'p95', 'p99', 'max'] }],
      ['get_kept', { kind: 'anomalies', stream: 'latency', where: { endpoint: '/api/checkout' }, from: '2026-09-28T10:00:00Z', to: '2026-09-28T11:00:00Z', order: 'unusual', limit: 5 }],
    ],
    read([w, k], x) {
      const r = csv(w.text)[0], top = csv(k.text)[0];
      const says = `p99 ${r.ms_p99} ms over ${Number(r.n).toLocaleString('en-US')} requests. The slowest, ${top.ms} ms at ${hm(top.time)}, is kept whole.`;
      const checks = [
        ['Requests in the hour', r.n, x.n, Number(r.n) === x.n],
        ['Average', r.ms_avg, x.avg.toFixed(7), near(Number(r.ms_avg), x.avg, 1e-9)],
        ['Slowest request', r.ms_max, x.max, Number(r.ms_max) === x.max],
        ['p50, within 1%', r.ms_p50, `${x.p50} (${pctApart(Number(r.ms_p50), x.p50)}% apart)`, near(Number(r.ms_p50), x.p50, 0.01), 'within 1%'],
        ['p95, within 1%', r.ms_p95, `${x.p95} (${pctApart(Number(r.ms_p95), x.p95)}% apart)`, near(Number(r.ms_p95), x.p95, 0.01), 'within 1%'],
        ['p99, within 1%', r.ms_p99, `${x.p99} (${pctApart(Number(r.ms_p99), x.p99)}% apart)`, near(Number(r.ms_p99), x.p99, 0.01), 'within 1%'],
        ['The slowest request is kept whole', top.ms, x.max, Number(top.ms) === x.max],
      ];
      return { says, checks };
    },
  },
  {
    id: 'trades', server: 'trades',
    ask: "What was SIM4's 1-minute candle at 10:30 New York time?",
    calls: [
      ['get_windows', { stream: 'trades', where: { symbol: 'SIM4' }, from: '2026-09-28T10:30:00-04:00', to: '2026-09-28T10:31:00-04:00', stats: ['n', 'first', 'max', 'min', 'last', 'sum'] }],
    ],
    read([w], x) {
      const r = csv(w.text)[0];
      const vwap = Number(r.notional_sum) / Number(r.size_sum);
      const says = `Open ${r.price_first}, high ${r.price_max}, low ${r.price_min}, close ${r.price_last}; ${Number(r.size_sum).toLocaleString('en-US')} shares in ${r.n} trades, VWAP ${vwap.toFixed(4)}.`;
      const checks = [
        ['Trades', r.n, x.n, Number(r.n) === x.n],
        ['Open, high, low, close', `${r.price_first} ${r.price_max} ${r.price_min} ${r.price_last}`, `${x.open} ${x.high} ${x.low} ${x.close}`,
          Number(r.price_first) === x.open && Number(r.price_max) === x.high && Number(r.price_min) === x.low && Number(r.price_last) === x.close],
        ['Volume', r.size_sum, x.volume, Number(r.size_sum) === x.volume],
        ['VWAP', vwap.toFixed(6), x.vwap.toFixed(6), near(vwap, x.vwap, 1e-9)],
      ];
      return { says, checks };
    },
  },
  {
    id: 'usage', server: 'usage',
    ask: 'What does Harbor Legal Drafts owe for September, line by line?',
    calls: [
      ['get_answer', { name: 'invoices', where: { customer: 'harbor' }, period: '2026-09' }],
      ['get_answer', { name: 'invoice_lines', where: { customer: 'harbor' }, period: '2026-09' }],
    ],
    read([inv, lines], x) {
      const r = csv(inv.text)[0], ls = csv(lines.text);
      const says = `${usd(r.due_nano)} due for ${Number(r.requests).toLocaleString('en-US')} requests on the ${r.plan} plan: ${ls.map((l) => `${l.model} ${usd(l.list_nano)}`).join(', ')}. Model cost ${usd(r.cost_nano)}.`;
      const sum = (k) => ls.reduce((a, l) => a + BigInt(l[k]), 0n);
      const checks = [
        ['Requests billed', r.requests, x.requests, Number(r.requests) === x.requests],
        ['Tokens in and out', `${r.input_tokens} ${r.output_tokens}`, `${x.input_tokens} ${x.output_tokens}`, Number(r.input_tokens) === x.input_tokens && Number(r.output_tokens) === x.output_tokens],
        ['Amount due, in billionths of a dollar', r.due_nano, x.due_nano, r.due_nano === x.due_nano],
        ['Model cost, in billionths of a dollar', r.cost_nano, x.cost_nano, r.cost_nano === x.cost_nano],
        ['The lines add up to the invoice', sum('list_nano').toString(), r.list_nano, sum('list_nano').toString() === r.list_nano && sum('requests') === BigInt(r.requests)],
      ];
      return { says, checks };
    },
  },
  {
    id: 'shop', server: 'shop',
    ask: 'Which log patterns are new since the payment incident began at 12:40, and what do the errors say?',
    calls: [
      ['get_kept', { kind: 'templates', from: '2026-09-29T12:40:00Z' }],
      ['get_kept', { kind: 'samples', stream: 'errors', where: { service: 'payments' }, from: '2026-09-29T12:40:00Z', limit: 3 }],
    ],
    read([t, s], x) {
      const rows = csv(t.text), samples = csv(s.text);
      const words = (t) => t.split(' ').filter((w) => !w.includes('=')).join(' ');
      const says = `${rows.length} new patterns: ${[...rows].reverse().map((r) => `${r.service} ${r.level} "${words(r.template)}" from ${hm(r.first_seen)}`).join(', ')}. The errors: "${(samples[0]?.line || '').split(' ').slice(3, 7).join(' ')}".`;
      const want = [...x.fresh].sort((a, b) => a.kind.localeCompare(b.kind));
      const got = rows.map((r) => ({ kind: `${r.level} ${r.service} ${r.template.split(' ').filter((w) => !/[=/0-9<]/.test(w)).slice(0, 2).join(' ')}`, first: r.first_seen, lines: Number(r.lines) }))
        .sort((a, b) => a.kind.localeCompare(b.kind));
      const same = got.length === want.length && got.every((g, i) => g.kind === want[i].kind && g.first === want[i].first.slice(0, 19) + 'Z' && g.lines === want[i].lines);
      const checks = [
        ['New patterns since 12:40', rows.length, want.length, rows.length === want.length],
        ['Each: level, service, first line and count', got.map((g) => `${g.kind} ${g.lines}`).join('; '), want.map((w) => `${w.kind} ${w.lines}`).join('; '), same],
        ['The error lines kept whole name the provider', `${samples.length} lines, ${[...new Set(samples.map((r) => (r.line.match(/provider=(\S+)/) || [])[1]))].join(', ')}`, 'northpay',
          samples.length > 0 && samples.every((r) => r.line.includes('charge failed provider=northpay'))],
      ];
      return { says, checks };
    },
  },
];

export class Run {
  // files: {name: Uint8Array} holding each server's SQLite file; go: the API of precomputing.wasm.
  constructor(sqlite3, go, files, facts) {
    this.sqlite3 = sqlite3;
    this.facts = facts;
    this.servers = {};
    for (const s of SERVERS) {
      const db = openReadOnly(sqlite3, files[s.name]);
      const server = go.mcp(sqliteReader(sqlite3, db), `Precomputing: ${s.name}.sqlite`);
      if (server.error) throw new Error(server.error);
      this.servers[s.name] = { ...s, db, server, fileBytes: files[s.name].byteLength };
    }
    this.nextId = 1;
  }

  // request sends one JSON-RPC message, as a 2026-07-28 client sends it over HTTP.
  request(server, method, params) {
    const id = this.nextId++;
    const request = { jsonrpc: '2.0', id, method, params: { ...params, _meta: META } };
    const headers = { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': PROTOCOL, 'Mcp-Method': method };
    if (method === 'tools/call') headers['Mcp-Name'] = params.name;
    return this.send(server, JSON.stringify(request), headers);
  }

  // send hands the server any message and headers, such as ones a visitor wrote.
  send(server, body, headers) {
    const t0 = performance.now();
    const res = this.servers[server].server.handle(body, headers);
    const ms = performance.now() - t0;
    let reply = null;
    try { reply = res.body ? JSON.parse(res.body) : null; } catch { /* shown as it came */ }
    return { server, headers, body, status: res.status, response: res.body, reply, ms };
  }

  // tool calls a tool and measures what the model would read: the text of the result.
  tool(server, name, args) {
    const x = this.request(server, 'tools/call', { name, arguments: args });
    const r = x.reply?.result;
    x.tool = name;
    x.args = args;
    x.text = r ? r.content.map((c) => c.text).join('') : x.reply?.error?.message || x.response;
    x.isError = !r || r.isError === true;
    x.bytes = utf8.encode(x.text).length;
    x.tokens = tokens(x.bytes);
    return x;
  }

  tools(server) { return this.request(server, 'tools/list', {}).reply.result.tools; }

  describe(server) { return this.tool(server, 'describe_file', {}); }

  // ask makes a question's calls and reads the answer against the facts.
  ask(i) {
    const q = QUESTIONS[i];
    const calls = q.calls.map(([name, args]) => this.tool(q.server, name, args));
    const fact = this.facts.questions[q.id];
    const { says, checks } = q.read(calls, fact.exact);
    const answerBytes = calls.reduce((a, c) => a + c.bytes, 0);
    return {
      i, id: q.id, server: q.server, ask: q.ask, calls, says,
      checks: checks.map(([what, got, want, ok, rule]) => ({ what, got: String(got), want: String(want), ok, rule: rule || 'equal' })),
      bytes: answerBytes, tokens: tokens(answerBytes),
      raw: { ...fact.raw, tokens: tokens(fact.raw.bytes) },
    };
  }

  // finish asks everything, the way the headless run and the results section see it.
  finish() {
    const describes = SERVERS.map((s) => this.describe(s.name));
    const answers = QUESTIONS.map((_, i) => this.ask(i));
    const checks = answers.flatMap((a) => a.checks);
    return {
      tools: this.tools(SERVERS[0].name).map((t) => t.name),
      describes: describes.map((d) => ({ server: d.server, bytes: d.bytes, tokens: d.tokens })),
      answers: answers.map(({ calls, ...a }) => ({ ...a, calls: calls.map((c) => ({ tool: c.tool, args: c.args, bytes: c.bytes, tokens: c.tokens, ms: c.ms, isError: c.isError, text: c.text })) })),
      checks: checks.length, passed: checks.filter((c) => c.ok).length,
      answerTokens: answers.reduce((a, x) => a + x.tokens, 0),
      describeTokens: describes.reduce((a, d) => a + d.tokens, 0),
      rawTokens: answers.reduce((a, x) => a + x.raw.tokens, 0),
      rawBytes: answers.reduce((a, x) => a + x.raw.bytes, 0),
      files: this.facts.files,
      sqlite: this.sqlite3.version.libVersion,
    };
  }

  exportFile(server) { return this.sqlite3.capi.sqlite3_js_db_export(this.servers[server].db.pointer); }
}
