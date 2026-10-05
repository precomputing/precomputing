// Runs Demo 7 off the page's main thread: SQLite WebAssembly holding the four files, read only,
// and one MCP server per file from precomputing.wasm.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo, startGo } from '../../lib/engine.js';
import { Run, SERVERS } from './run.js';

let run = null;
const post = (msg, transfer) => self.postMessage(msg, transfer || []);

// fetchFile loads one file, decompressing it unless the server already did.
async function fetchFile(url, onBytes) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  const buf = new Uint8Array(await res.arrayBuffer());
  onBytes(buf.byteLength);
  if (buf[0] === 0x1f && buf[1] === 0x8b) {
    const stream = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip'));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }
  return buf;
}

async function init() {
  post({ type: 'status', text: 'Loading SQLite and the MCP server' });
  const [sqlite3, module, facts] = await Promise.all([
    sqlite3InitModule({ print: () => {}, printErr: () => {} }),
    loadGo(new URL('../../lib/go/', import.meta.url).href),
    fetch('data/facts.json').then((r) => { if (!r.ok) throw new Error(`facts.json: HTTP ${r.status}`); return r.json(); }),
  ]);
  const go = await startGo(module);
  const files = {};
  let loaded = 0;
  const total = facts.files.reduce((a, f) => a + f.gzBytes, 0);
  for (const s of SERVERS) {
    post({ type: 'status', text: `Loading the four files: ${s.name}` });
    files[s.name] = await fetchFile(`data/${s.name}.sqlite.gz`, (n) => { loaded += n; post({ type: 'progress', loaded, total }); });
  }
  run = new Run(sqlite3, go, files, facts);
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: go.version, facts, tools: run.tools('latency') });
}

// Every request carries an id, and its reply carries it back.
self.onmessage = (ev) => {
  const m = ev.data;
  const reply = (result, transfer) => post({ type: 'reply', id: m.id, result }, transfer);
  try {
    switch (m.type) {
      case 'describe': reply(run.describe(m.server)); break;
      case 'ask': reply(run.ask(m.i)); break;
      case 'tool': reply(run.tool(m.server, m.tool, m.args)); break;
      case 'send': reply(run.send(m.server, m.body, m.headers)); break;
      case 'export': { const bytes = run.exportFile(m.server); reply(bytes, [bytes.buffer]); break; }
      default: post({ type: 'reply', id: m.id, error: `unknown request ${m.type}` });
    }
  } catch (e) {
    post({ type: 'reply', id: m.id, error: String(e.message || e) });
  }
};

init().catch((e) => post({ type: 'error', message: `The demo could not start: ${e.message || e}` }));
