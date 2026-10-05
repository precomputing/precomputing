// Runs Demo 1 off the page's main thread: SQLite WebAssembly, the compiled policy and the scenario.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { Run } from './run.js';

const SPEED = 240;         // simulated seconds per real second: three hours in about 45 seconds
const TICK_MS = 250;       // how often the page gets fresh figures

let run = null;
let playing = false;
let fullSpeed = false;
let anchorWall = 0;        // real time and simulated time at the last play or speed change
let anchorSim = 0;
let lastTick = 0;
let looping = false;

const post = (msg, transfer) => self.postMessage(msg, transfer || []);

async function init() {
  const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const [policy, schema, distill] = await Promise.all(
    ['policy.precompute', 'policy.sql', 'policy.distill.sql'].map((f) => fetch(f).then((r) => {
      if (!r.ok) throw new Error(`${f}: HTTP ${r.status}`);
      return r.text();
    })));
  run = new Run(sqlite3, { schema, distill });
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, policy, schema, distill, gamma: run.gamma });
  post({ type: 'snapshot', ...run.snapshot() });
}

function yieldNow() { return new Promise((r) => setTimeout(r, 0)); }

async function loop() {
  if (looping) return;
  looping = true;
  try {
    while (playing && !run.done) {
      const target = fullSpeed ? Infinity : anchorSim + ((performance.now() - anchorWall) / 1000) * SPEED;
      const want = Math.floor(target - run.scenario.t);
      if (want > 0) run.step(Math.min(want, 1e6), 60);
      else await new Promise((r) => setTimeout(r, 16));
      if (performance.now() - lastTick > TICK_MS || run.done) {
        lastTick = performance.now();
        post({ type: 'snapshot', ...run.snapshot() });
      }
      await yieldNow();
    }
    if (run.done && playing) {
      playing = false;
      post({ type: 'finishing' });
      await yieldNow();
      const results = run.finish();
      post({ type: 'snapshot', ...run.snapshot() });
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
  anchorWall = performance.now();
  anchorSim = run.scenario.t;
}

self.onmessage = async (ev) => {
  const m = ev.data;
  try {
    switch (m.type) {
      case 'play':
        if (run.done) { run.reset(); post({ type: 'snapshot', ...run.snapshot() }); }
        playing = true; fullSpeed = false; anchor(); loop();
        break;
      case 'pause':
        playing = false;
        post({ type: 'snapshot', ...run.snapshot() });
        break;
      case 'stop':
        playing = false; fullSpeed = false;
        while (looping) await new Promise((r) => setTimeout(r, 5));
        run.reset();
        post({ type: 'snapshot', ...run.snapshot() });
        break;
      case 'review':
        playing = true; fullSpeed = true; loop();
        break;
      case 'traffic':
        run.setTraffic(m.rate); anchor();
        break;
      case 'spike': {
        const sp = run.injectSpike();
        post({ type: 'event', t: run.scenario.t, kind: 'spike', text: `Your spike: ${sp.endpoint} took ${sp.ms.toLocaleString('en-US')} ms` });
        break;
      }
      case 'outage': {
        const o = run.startOutage(m.endpoint);
        post({ type: 'event', t: run.scenario.t, kind: 'outage', text: `Your outage: ${m.name} six times slower for two minutes`, outage: o });
        break;
      }
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

init().catch((e) => post({ type: 'error', message: `The engine could not start: ${e.message || e}` }));
