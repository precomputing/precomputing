// Runs Demo 4 off the page's main thread: SQLite WebAssembly, the log reducer and the Engine
// (Go, built for the browser), and the web shop's two hours of logs.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo } from '../../lib/engine.js';
import { Run } from './run.js';
import { INCIDENT, DEPLOY } from './scenario.js';

const SPEED = 120;         // simulated seconds per real second: the two hours in about a minute
const SLOW = 20;           // around the incident, the deploy and a broken shard
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
  const w = [[INCIDENT.from - 30, INCIDENT.from + 90], [DEPLOY.at - 20, DEPLOY.at + 40]];
  for (const s of run.shards) w.push([s.from - 5, s.from + 60]);
  return w.some(([a, b]) => t >= a && t < b);
}

async function init() {
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const base = new URL('../../lib/go/', import.meta.url).href;
  const text = (f) => fetch(f).then((r) => {
    if (!r.ok) throw new Error(`${f}: HTTP ${r.status}`);
    return r.text();
  });
  const [module, policy, dashboard] = await Promise.all([loadGo(base), text('shop.precompute'), text('shop-dashboard.json')]);
  run = await new Run(sqlite3, module, { policy, dashboard }).init();
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: run.go.version, policy, dashboard,
    schema: run.compiled.schema, distill: run.compiled.distill, plans: run.plans });
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
      case 'rate':
        run.setRate(m.rate);
        break;
      case 'shard':
        run.breakShard();
        snapshot();
        break;
      case 'search':
        post({ type: 'search', id: m.id, result: run.search(m.text) });
        break;
      case 'import':
        post({ type: 'import', id: m.id, result: run.importDashboard(m.text) });
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
