// Records a session of an agent working through MCP, for Demo 7's page.
//
//   node tools/mcp-session.mjs start DIR       serve DIR/{latency,trades,usage,shop}.sqlite read only,
//                                              one `precomputing serve` each, with a read token
//   node tools/mcp-session.mjs question TEXT   begin a turn
//   node tools/mcp-session.mjs call SERVER TOOL 'ARGS' [WHY]
//                                              one tool call through the official MCP client (v2,
//                                              protocol 2026-07-28); prints the result and logs it
//   node tools/mcp-session.mjs answer TEXT     end the turn with the agent's answer
//   node tools/mcp-session.mjs stop            stop the servers
//   node tools/mcp-session.mjs build [OUT]     write the page's session.json from the log
//
// The log (build/session/log.jsonl) holds every call and result as they happened.
import { spawn } from 'node:child_process';
import { appendFileSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

const root = new URL('..', import.meta.url);
const dir = new URL('build/session/', root);
mkdirSync(dir, { recursive: true });
const cfgPath = new URL('servers.json', dir), logPath = new URL('log.jsonl', dir);
const SERVERS = ['latency', 'trades', 'usage', 'shop'];
const [cmd, ...rest] = process.argv.slice(2);
const log = (entry) => appendFileSync(logPath, JSON.stringify({ at: new Date().toISOString(), ...entry }) + '\n');

if (cmd === 'start') {
  const files = resolve(rest[0]);
  const token = [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, '0')).join('');
  writeFileSync(new URL('tokens', dir), `read ${token} session\n`);
  const cfg = { token, servers: {} };
  for (const [i, name] of SERVERS.entries()) {
    const port = 18600 + i;
    const p = spawn(resolve(new URL('build/precomputing', root).pathname), ['serve', '--read-only', '--addr', `127.0.0.1:${port}`,
      '--token-file', new URL('tokens', dir).pathname, `${files}/${name}.sqlite`], { detached: true, stdio: 'ignore' });
    p.unref();
    cfg.servers[name] = { url: `http://127.0.0.1:${port}/mcp`, pid: p.pid };
  }
  writeFileSync(cfgPath, JSON.stringify(cfg, null, 1));
  log({ kind: 'start', files: SERVERS.map((s) => `${s}.sqlite`) });
  console.log('serving', Object.entries(cfg.servers).map(([k, v]) => `${k} ${v.url}`).join(', '));
} else if (cmd === 'stop') {
  const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'));
  for (const s of Object.values(cfg.servers)) { try { process.kill(s.pid, 'SIGTERM'); } catch { /* gone */ } }
  console.log('stopped');
} else if (cmd === 'question' || cmd === 'answer') {
  log({ kind: cmd, text: rest.join(' ') });
} else if (cmd === 'call') {
  const [server, tool, argsText, why] = rest;
  const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'));
  const client = new Client({ name: 'demo-5-session', version: '1' }, { versionNegotiation: { mode: { pin: '2026-07-28' } } });
  await client.connect(new StreamableHTTPClientTransport(new URL(cfg.servers[server].url), { requestInit: { headers: { Authorization: `Bearer ${cfg.token}` } } }));
  const args = JSON.parse(argsText || '{}');
  const res = await client.callTool({ name: tool, arguments: args });
  const text = res.content.map((c) => c.text).join('');
  log({ kind: 'call', server, tool, args, why: why || '', text, isError: res.isError === true, version: client.getNegotiatedProtocolVersion() });
  await client.close();
  console.log(text);
  console.log(`--- ${Buffer.byteLength(text)} bytes${res.isError ? ', a tool error' : ''}`);
} else if (cmd === 'build') {
  const out = rest[0] || new URL('demo/mcp/app/session.json', root).pathname;
  const entries = readFileSync(logPath, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  const turns = [];
  let turn = null;
  for (const e of entries) {
    if (e.kind === 'question') turns.push((turn = { question: e.text, steps: [], answer: '', at: e.at }));
    else if (e.kind === 'call' && turn) turn.steps.push({ server: e.server, tool: e.tool, args: e.args, why: e.why, text: e.text, isError: e.isError, bytes: Buffer.byteLength(e.text) });
    else if (e.kind === 'answer' && turn) turn.answer = e.text;
  }
  const first = entries.find((e) => e.kind === 'call');
  const session = {
    recorded: entries[0].at.slice(0, 10),
    protocol: first?.version,
    intro: `Recorded on ${new Date(entries[0].at).toLocaleDateString('en-GB', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' })}. `
      + 'The agent is Claude, made by Anthropic, connected to four precomputing serve --read-only servers, one for each file above, over HTTP with a read token, '
      + `through the official MCP TypeScript client (version 2.2.0, protocol ${first?.version}). `
      + 'Claude built this release and had seen these files while building it; in this session it read them only through the tools. The questions were chosen before the first call. '
      + 'Every call, result and answer is shown as it happened, and each note on why a call was made was written with the call.',
    turns,
  };
  writeFileSync(out, JSON.stringify(session, null, 1) + '\n');
  console.log(`wrote ${out}: ${turns.length} turns, ${turns.reduce((n, t) => n + t.steps.length, 0)} calls`);
} else {
  console.log('usage: start DIR | question TEXT | call SERVER TOOL ARGS [WHY] | answer TEXT | stop | build [OUT]');
}
