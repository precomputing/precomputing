// Runs Demo 5 off the page's main thread: SQLite WebAssembly and the Engine (Go, built for the
// browser). The page reads the stream and hands the changes over; the simulator runs here.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo } from '../../lib/engine.js';
import { Run } from './run.js';
import { Simulator, setSalt } from './wiki.js';

const TICK_MS = 1000;       // the Engine writes its file and the page reads it every second
const CHECK_MS = 30000;     // the recount checks what could have changed every 30 seconds
const MARGIN = 300;         // a check looks back this far, for changes that arrive late

let run = null;
let filter = 'wikipedias';
let sim = null;
let simTimer = null;
let simTs = 0;
let lastCheck = 0;
let lastWikis = 0;
let dirty = false;

const post = (msg, transfer) => self.postMessage(msg, transfer || []);

function snapshot() {
  const s = run.snapshot(filter);
  if (performance.now() - lastWikis > 10000) { s.wikiList = run.wikiList(); lastWikis = performance.now(); }
  post({ type: 'snapshot', ...s });
}

function tick() {
  try {
    if (dirty) {
      run.checkpoint();
      dirty = false;
      if (performance.now() - lastCheck > CHECK_MS && run.t) {
        run.check(run.lastCheck ? run.lastCheck.at - MARGIN : null);
        lastCheck = performance.now();
      }
    }
    snapshot();
  } catch (e) {
    post({ type: 'error', message: String(e.message || e) });
  }
}

function simulate(on) {
  if (simTimer) { clearInterval(simTimer); simTimer = null; }
  if (!on) return;
  sim ??= new Simulator((Date.now() / 1000) | 0);
  simTs = Math.floor(Date.now() / 1000) - 1;
  simTimer = setInterval(() => {
    const until = Math.floor(Date.now() / 1000);
    for (; simTs < until; simTs++) for (const e of sim.second(simTs + 1)) run.add(e);
    dirty = true;
  }, 200);
}

async function init() {
  setSalt(crypto.getRandomValues(new Uint32Array(1))[0]);
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const base = new URL('../../lib/go/', import.meta.url).href;
  const [module, policy] = await Promise.all([loadGo(base), fetch('policy.precompute').then((r) => {
    if (!r.ok) throw new Error(`policy.precompute: HTTP ${r.status}`);
    return r.text();
  })]);
  run = await new Run(sqlite3, module, policy).init();
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: run.go.version, policy, schema: run.compiled.schema || '', distill: run.compiled.distill || '' });
  snapshot();
  setInterval(tick, TICK_MS);
}

self.onmessage = (ev) => {
  const m = ev.data;
  try {
    switch (m.type) {
      case 'lines':
        for (const text of m.lines) {
          let e = null;
          try { e = JSON.parse(text); } catch { run.counts.unread++; continue; }
          run.add(e);
        }
        dirty = true;
        break;
      case 'sim':
        simulate(m.on);
        break;
      case 'filter':
        filter = m.filter;
        snapshot();
        break;
      case 'clear':
        simulate(false);
        sim = null;
        run.reset();
        lastCheck = 0;
        snapshot();
        break;
      case 'check': {
        if (dirty) { run.checkpoint(); dirty = false; }
        const r = run.t ? run.check() : null;
        lastCheck = performance.now();
        post({ type: 'check', result: r });
        snapshot();
        break;
      }
      case 'query':
        if (dirty) { run.checkpoint(); dirty = false; }
        post({ type: 'query', id: m.id, result: run.query(m.sql) });
        break;
      case 'export': {
        if (dirty) { run.checkpoint(); dirty = false; }
        const bytes = run.exportFile();
        post({ type: 'export', bytes }, [bytes.buffer]);
        break;
      }
    }
  } catch (e) {
    post({ type: 'error', message: String(e.message || e) });
  }
};

init().catch((e) => post({ type: 'error', message: `The demo could not start: ${e.message || e}` }));
