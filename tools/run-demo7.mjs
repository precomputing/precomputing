// Runs Demo 7 headless in Node with the demo's own code (demo/mcp/app/run.js): the four questions,
// each call answered by the MCP server of the WebAssembly build, then every check. With --native,
// the same calls also go to the native binary over stdio (`precomputing mcp FILE`), and the two
// builds' answers are compared byte for byte. These are the numbers the site publishes.
// Usage: node tools/run-demo7.mjs [--json] [--native build/precomputing]
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { gunzipSync } from 'node:zlib';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { loadGo, startGo } from '../demo/lib/engine.js';
import { Run, SERVERS, QUESTIONS, PROTOCOL } from '../demo/mcp/app/run.js';

const json = process.argv.includes('--json');
const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const log = (...a) => { if (!json) console.log(...a); };
const root = new URL('..', import.meta.url);
const data = new URL('demo/mcp/app/data/', root);
const sqlite3 = await sqlite3InitModule({ print() {}, printErr() {} });
const go = await startGo(await loadGo(new URL('demo/lib/go/', root).href));
const facts = JSON.parse(readFileSync(new URL('facts.json', data), 'utf8'));
const files = Object.fromEntries(SERVERS.map((s) => [s.name, new Uint8Array(gunzipSync(readFileSync(new URL(`${s.name}.sqlite.gz`, data))))]));
const t0 = performance.now();
const run = new Run(sqlite3, go, files, facts);
const r = run.finish();
r.wallMs = performance.now() - t0;

// The native build, over stdio, on the same files.
const native = arg('--native');
if (native) {
  const dir = mkdtempSync(join(tmpdir(), 'demo7-'));
  const meta = { 'io.modelcontextprotocol/protocolVersion': PROTOCOL, 'io.modelcontextprotocol/clientCapabilities': {} };
  let compared = 0, differ = 0;
  const diffs = [];
  for (const s of SERVERS) {
    const path = join(dir, `${s.name}.sqlite`);
    writeFileSync(path, files[s.name]);
    const calls = [['describe_file', {}], ...QUESTIONS.filter((q) => q.server === s.name).flatMap((q) => q.calls)];
    const lines = calls.map(([name, args], i) => JSON.stringify({ jsonrpc: '2.0', id: i + 1, method: 'tools/call', params: { name, arguments: args, _meta: meta } }));
    lines.push(JSON.stringify({ jsonrpc: '2.0', id: 999, method: 'tools/list', params: { _meta: meta } }));
    const out = spawnSync(native, ['mcp', path], { input: lines.join('\n') + '\n', encoding: 'utf8' });
    const replies = out.stdout.trim().split('\n').map((l) => JSON.parse(l));
    for (const [i, [name, args]] of calls.entries()) {
      const nat = replies[i].result.content[0].text;
      const wasm = run.tool(s.name, name, args).text;
      compared++;
      if (nat !== wasm) { differ++; diffs.push({ server: s.name, tool: name, native: nat, wasm }); }
    }
    const natTools = JSON.stringify(replies[replies.length - 1].result.tools);
    const wasmTools = JSON.stringify(run.tools(s.name));
    compared++;
    if (natTools !== wasmTools) { differ++; diffs.push({ server: s.name, tool: 'tools/list' }); }
  }
  r.native = { compared, differ, diffs };
}

if (json) {
  console.log(JSON.stringify(r, null, 1));
  process.exit(r.passed === r.checks && (!r.native || r.native.differ === 0) ? 0 : 1);
}
log(`SQLite ${r.sqlite}; tools: ${r.tools.join(', ')}`);
for (const f of r.files) log(`  ${f.name.padEnd(8)} ${String(f.fullBytes).padStart(9)} bytes at the end of Demo ${f.demo}, ${String(f.fadedBytes).padStart(8)} ${f.when}, ${f.gzBytes} compressed`);
for (const d of r.describes) log(`  describe_file ${d.server.padEnd(8)} ${d.bytes} bytes, about ${d.tokens} tokens`);
for (const a of r.answers) {
  log(`\n${a.ask}`);
  for (const c of a.calls) log(`  ${c.tool} ${JSON.stringify(c.args)}: ${c.bytes} bytes, ${c.ms.toFixed(2)} ms${c.isError ? ' ERROR' : ''}`);
  log(`  ${a.says}`);
  log(`  answer ${a.bytes} bytes (about ${a.tokens} tokens); the raw data: ${a.raw.events} events, ${a.raw.bytes} bytes (about ${a.raw.tokens} tokens), ${Math.round(a.raw.bytes / a.bytes)} times more`);
  for (const c of a.checks) log(`    ${c.ok ? 'ok  ' : 'DIFF'} ${c.what}: ${c.got} against ${c.want}`);
}
log(`\n${r.passed} of ${r.checks} checks pass. Answers ${r.answerTokens} tokens, descriptions ${r.describeTokens}, raw data ${r.rawTokens}.`);
if (r.native) log(`native against WebAssembly: ${r.native.compared} answers compared, ${r.native.differ} differ`, r.native.diffs.slice(0, 2));
process.exit(r.passed === r.checks && (!r.native || r.native.differ === 0) ? 0 : 1);
