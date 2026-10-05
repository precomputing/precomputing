// Runs Demo 8 off the page's main thread: SQLite WebAssembly, the store of agent calls and the
// Engine (Go, built for the browser), and the day of agent runs.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo } from '../../lib/engine.js';
import { Run, STUCK } from './run.js';

const SPEED = 600;         // seconds of the day per real second: 07:00 to 20:00 in under a minute and a half
const SLOW = 60;           // around a run stuck in a loop
const TICK_MS = 250;

let run = null;
let playing = false;
let fullSpeed = false;
let target = 0;
let lastWall = 0;
let lastTick = 0;
let looping = false;
let slowWindows = [];

const post = (msg, transfer) => self.postMessage(msg, transfer || []);
const snapshot = () => post({ type: 'snapshot', ...run.snapshot() });
const yieldNow = () => new Promise((r) => setTimeout(r, 0));
const slowMotion = (t) => slowWindows.some(([a, b]) => t >= a && t < b);

// gunzip reads the day's runs, decompressing them unless the server already did.
async function gunzip(res) {
  const buf = new Uint8Array(await res.arrayBuffer());
  if (buf[0] !== 0x1f || buf[1] !== 0x8b) return new TextDecoder().decode(buf);
  const stream = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).text();
}

async function init() {
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const base = new URL('../../lib/go/', import.meta.url).href;
  const get = async (f) => {
    const r = await fetch(f);
    if (!r.ok) throw new Error(`${f}: HTTP ${r.status}`);
    return r;
  };
  const [module, policy, day, meta] = await Promise.all([loadGo(base), get('policy.precompute').then((r) => r.text()),
    get('data/day.jsonl.gz').then(gunzip), get('data/meta.json').then((r) => r.json())]);
  run = await new Run(sqlite3, module, { policy, day }).init();
  slowWindows = run.runs.filter((r) => r.replies.length >= STUCK).map((r) => [r.start - 120, r.start + r.messages[r.messages.length - 1].at + 60]);
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: run.go.version, policy, meta,
    schema: run.compiled.schema, traceSchema: run.traceSchema(), patterns: run.eng.patterns(),
    runs: run.runs.map((r) => ({ id: r.id, repo: r.repo, calls: r.replies.length, start: r.start })) });
  snapshot();
}

async function loop() {
  if (looping) return;
  looping = true;
  try {
    while (playing && !run.done) {
      const wall = performance.now();
      const speed = slowMotion(run.t) ? SLOW : SPEED;
      target += ((wall - lastWall) / 1000) * speed;
      lastWall = wall;
      target = Math.min(target, run.t + 2 * speed);
      const want = fullSpeed ? 1e9 : Math.floor(target - run.t);
      if (want >= 1) run.step(want, 60);
      else await new Promise((r) => setTimeout(r, 16));
      if (performance.now() - lastTick > TICK_MS || run.done) {
        lastTick = performance.now();
        const s = run.snapshot(!fullSpeed);
        s.slow = !fullSpeed && slowMotion(run.t);
        post({ type: 'snapshot', ...s });
      }
      await yieldNow();
    }
    if (run.done && playing) {
      playing = false;
      post({ type: 'finishing' });
      await yieldNow();
      const results = run.finish();
      snapshot();
      post({ type: 'results', ...results });
    }
  } catch (e) {
    playing = false;
    post({ type: 'error', message: String(e.message || e) });
  } finally {
    looping = false;
  }
}

function anchor() {
  lastWall = performance.now();
  target = run.t;
}

self.onmessage = async (ev) => {
  const m = ev.data;
  try {
    switch (m.type) {
      case 'play':
        if (run.done) { run.reset(); snapshot(); }
        playing = true; fullSpeed = false; anchor(); loop();
        break;
      case 'pause':
        playing = false;
        snapshot();
        break;
      case 'stop':
        playing = false; fullSpeed = false;
        while (looping) await new Promise((r) => setTimeout(r, 5));
        run.reset();
        snapshot();
        break;
      case 'review':
        playing = true; fullSpeed = true; loop();
        break;
      case 'again':
        run.reportAgain();
        snapshot();
        break;
      case 'raise':
        run.raise(m.repo);
        snapshot();
        break;
      case 'rebuild':
        post({ type: 'rebuild', id: m.id, result: await run.rebuild(m.callId) });
        break;
      case 'query':
        post({ type: 'query', id: m.id, result: run.query(m.sql) });
        break;
      case 'export': {
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
