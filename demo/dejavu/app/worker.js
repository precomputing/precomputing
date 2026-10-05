// Runs Demo 6 off the page's main thread: SQLite WebAssembly, the log reducer and the Engine (Go,
// built for the browser), yesterday's file and the web shop's two hours of logs today.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo } from '../../lib/engine.js';
import { Run } from './run.js';
import { gunzip } from './history.js';
import { TODAY_LABEL } from './scenario.js';

const SPEED = 120;         // simulated seconds per real second: the two hours in about a minute
const SLOW = 15;           // as an incident begins and when the on-call team writes one down
const TICK_MS = 250;

let run = null;
let playing = false;
let fullSpeed = false;
let target = 0;
let lastWall = 0;
let lastTick = 0;
let looping = false;

const post = (msg, transfer) => self.postMessage(msg, transfer || []);
const snapshot = () => post({ type: 'snapshot', ...run.snapshot() });
const yieldNow = () => new Promise((r) => setTimeout(r, 0));

function slowMotion(t) {
  if (t >= TODAY_LABEL.at - 10 && t < TODAY_LABEL.at + 20) return true;
  return run.truth.some((x) => t >= x.at - 15 && t < x.at + 75);
}

async function init() {
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const base = new URL('../../lib/go/', import.meta.url).href;
  const get = (f) => fetch(f).then((r) => {
    if (!r.ok) throw new Error(`${f}: HTTP ${r.status}`);
    return r;
  });
  const day = self.location.hash === '#artifact'
    ? get('history.b64.txt').then((r) => r.text()).then((t) => Uint8Array.from(atob(t.trim()), (c) => c.charCodeAt(0)))
    : get('history.db.gz').then((r) => r.arrayBuffer()).then((b) => new Uint8Array(b));
  const [module, gz, dejavuSql] = await Promise.all([loadGo(base), day, get('dejavu.sql').then((r) => r.text())]);
  const history = await gunzip(gz);
  run = await new Run(sqlite3, module, history).init();
  const policy = run.policy();
  const compiled = run.go.compile(policy, 'dejavu.precompute');
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: run.go.version, policy, dejavuSql,
    schema: compiled.schema || '', distill: compiled.distill || '', historyLines: run.historyLines, historyBytes: history.byteLength });
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
        const s = run.snapshot();
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
      case 'start': {
        const x = run.start(m.kind, m.opts || {});
        post({ type: 'start', ok: !!x });
        anchor();
        snapshot();
        break;
      }
      case 'label':
        post({ type: 'label', id: m.id, result: run.label(m.episode, m.title, m.fix) });
        snapshot();
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
