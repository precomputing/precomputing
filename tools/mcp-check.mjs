// Checks that Precomputing speaks MCP the way the official TypeScript SDK expects, in both eras
// of the protocol: the v2 client pinned to 2026-07-28, the v2 client probing ('auto'), and the
// v1 client, which opens with the initialize handshake (2025-11-25). Over HTTP it runs
// `precomputing serve --read-only` with a token file; over stdio it starts `precomputing mcp`.
// Every client lists the tools and calls each one, and the answers must match.
// Usage: node tools/mcp-check.mjs build/precomputing FILE [--json]
import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { Client as Client2, StreamableHTTPClientTransport as HTTP2 } from '@modelcontextprotocol/client';
import { StdioClientTransport as Stdio2 } from '@modelcontextprotocol/client/stdio';
import { Client as Client1 } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport as HTTP1 } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { StdioClientTransport as Stdio1 } from '@modelcontextprotocol/sdk/client/stdio.js';

const [bin, file] = process.argv.slice(2).filter((a) => !a.startsWith('--')).map((p) => resolve(p));
const json = process.argv.includes('--json');
const log = (...a) => { if (!json) console.log(...a); };
const token = 'check-' + Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
const dir = mkdtempSync(join(tmpdir(), 'mcp-check-'));
writeFileSync(join(dir, 'tokens'), `read ${token} mcp-check\n`);
const port = 18000 + Math.floor(Math.random() * 1000);
const url = `http://127.0.0.1:${port}/mcp`;

const server = spawn(bin, ['serve', '--read-only', '--addr', `127.0.0.1:${port}`, '--token-file', join(dir, 'tokens'), file], { stdio: ['ignore', 'ignore', 'pipe'] });
await new Promise((ok, fail) => {
  const t = setTimeout(() => fail(new Error('the server did not start')), 15000);
  server.stderr.on('data', (d) => { if (String(d).includes('serving')) { clearTimeout(t); ok(); } });
  server.on('exit', (c) => fail(new Error(`the server exited with ${c}`)));
});

const calls = [
  ['describe_file', {}],
  ['get_answer', null], // filled in from describe_file: the file's first precompute
  ['get_windows', null],
  ['get_kept', null],
  ['query', { sql: 'SELECT name, kind FROM _precomputing_objects ORDER BY name', limit: 5 }],
];

async function exercise(label, client, transport) {
  const t0 = performance.now();
  await client.connect(transport);
  const version = client.getNegotiatedProtocolVersion?.() ?? transport.protocolVersion ?? '2025-11-25';
  const era = client.getProtocolEra?.() ?? 'legacy';
  const { tools } = await client.listTools();
  const texts = [];
  let describe = '';
  for (let [name, fixed] of calls) {
    let args = fixed;
    if (!args) {
      const pc = describe.match(/^- (\w+) = /m)?.[1];
      const stream = describe.match(/^Stream (\w+)/m)?.[1];
      if (name === 'get_answer') {
        if (pc) args = { name: pc, limit: 3 };
        else { name = 'get_kept'; args = { kind: 'templates', limit: 3 }; } // a file with no precomputes, such as Demo 4's
      }
      if (name === 'get_windows') args = { stream, every: 'all', by: [], stats: ['n'] };
      const kept = describe.split('\n').find((l) => l.includes('Kept whole')) || '';
      if (name === 'get_kept') args = { kind: kept.includes('unusual') ? 'anomalies' : kept.includes('samples per') ? 'samples' : 'raw', stream, limit: 2 };
    }
    const res = await client.callTool({ name, arguments: args });
    const text = res.content.map((c) => c.text).join('');
    if (res.isError) throw new Error(`${label}: ${name} failed: ${text}`);
    if (name === 'describe_file') describe = text;
    texts.push(text);
  }
  await client.close();
  return { label, era, version, tools: tools.map((t) => t.name), texts, ms: performance.now() - t0 };
}

const headers = { Authorization: `Bearer ${token}` };
const runs = [];
try {
  // Without the token the server refuses; a read token may not post events.
  const denied = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
  const writing = await fetch(`http://127.0.0.1:${port}/v1/events`, { method: 'POST', headers, body: '' });
  runs.push(await exercise('v2 client, pinned to 2026-07-28, HTTP', new Client2({ name: 'mcp-check', version: '1' }, { versionNegotiation: { mode: { pin: '2026-07-28' } } }), new HTTP2(new URL(url), { requestInit: { headers } })));
  runs.push(await exercise('v2 client, auto, HTTP', new Client2({ name: 'mcp-check', version: '1' }, { versionNegotiation: { mode: 'auto' } }), new HTTP2(new URL(url), { requestInit: { headers } })));
  runs.push(await exercise('v1 client (handshake), HTTP', new Client1({ name: 'mcp-check', version: '1' }), new HTTP1(new URL(url), { requestInit: { headers } })));
  runs.push(await exercise('v2 client, auto, stdio', new Client2({ name: 'mcp-check', version: '1' }, { versionNegotiation: { mode: 'auto' } }), new Stdio2({ command: bin, args: ['mcp', file] })));
  runs.push(await exercise('v1 client (handshake), stdio', new Client1({ name: 'mcp-check', version: '1' }), new Stdio1({ command: bin, args: ['mcp', file] })));
  const same = runs.every((r) => JSON.stringify(r.texts) === JSON.stringify(runs[0].texts));
  const out = {
    file, denied: denied.status, readTokenPosting: writing.status, same,
    runs: runs.map(({ texts, ...r }) => ({ ...r, calls: texts.length, bytes: texts.reduce((a, t) => a + Buffer.byteLength(t), 0) })),
  };
  if (json) console.log(JSON.stringify(out, null, 1));
  log(`no token: HTTP ${denied.status}; a read token posting events: HTTP ${writing.status}`);
  for (const r of out.runs) log(`${r.label.padEnd(40)} era ${String(r.era).padEnd(7)} version ${String(r.version).padEnd(11)} ${r.tools.length} tools, ${r.calls} calls, ${r.bytes} bytes, ${Math.round(r.ms)} ms`);
  log(same ? 'Every client got the same answers.' : 'THE ANSWERS DIFFER between clients');
  process.exitCode = same && denied.status === 401 && writing.status === 403 ? 0 : 1;
} finally {
  server.kill('SIGTERM');
}
