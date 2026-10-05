// Runs Demo 3 off the page's main thread: SQLite WebAssembly, the compiled policy, the billing
// tables and the month of requests.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { Run } from './run.js';
import { START } from './scenario.js';

const SPEED = 43200;       // simulated seconds per real second: twelve hours, the month in about a minute
const SLOW = 4320;         // around the month's events and the visitor's: seventy-two minutes a second
const TICK_MS = 250;       // how often the page gets fresh figures

let run = null;
let playing = false;
let fullSpeed = false;
let target = 0;            // the simulated time (seconds since START) the clock has reached
let lastWall = 0;
let lastTick = 0;
let looping = false;

const post = (msg, transfer) => self.postMessage(msg, transfer || []);
const snapshot = () => post({ type: 'snapshot', ...run.snapshot() });

async function init() {
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const [policy, schema, distill, billing] = await Promise.all(
    ['policy.precompute', 'policy.sql', 'policy.distill.sql', 'billing.sql'].map((f) => fetch(f).then((r) => {
      if (!r.ok) throw new Error(`${f}: HTTP ${r.status}`);
      return r.text();
    })));
  run = new Run(sqlite3, { schema, distill, billing });
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, policy, schema, distill, billing });
  snapshot();
}

function yieldNow() { return new Promise((r) => setTimeout(r, 0)); }

async function loop() {
  if (looping) return;
  looping = true;
  try {
    while (playing && !run.done) {
      const wall = performance.now();
      const speed = run.slowMotion() ? SLOW : SPEED;
      target += ((wall - lastWall) / 1000) * speed;
      lastWall = wall;
      const sim = run.t - START;
      target = Math.min(target, sim + 2 * speed); // when the device falls behind, catch up gently
      const want = fullSpeed ? 1e9 : Math.floor(target - sim);
      if (want >= 60) run.step(want, 60);
      else await new Promise((r) => setTimeout(r, 16));
      if (performance.now() - lastTick > TICK_MS || run.done) {
        lastTick = performance.now();
        snapshot();
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
  target = run.t - START;
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
      case 'storm':
        run.retryStorm();
        snapshot();
        break;
      case 'cut':
        run.cutLink(m.gateway);
        snapshot();
        break;
      case 'quota':
        run.raiseQuota(m.customer);
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

init().catch((e) => post({ type: 'error', message: `The meter could not start: ${e.message || e}` }));
