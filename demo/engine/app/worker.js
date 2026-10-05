// Runs Demo 2 off the page's main thread: SQLite WebAssembly, the Engine (Go, built for the
// browser) and the trading day.
import sqlite3InitModule from '../../lib/sqlite/index.mjs';
import { loadGo } from '../../lib/engine.js';
import { Run, Race } from './run.js';

const SPEED = 390;          // simulated seconds per real second: the trading day in a minute
const TICK_MS = 250;        // how often the page gets fresh figures

let sqlite3 = null;
let run = null;
let playing = false;
let fullSpeed = false;
let anchorWall = 0;
let anchorSim = 0;
let lastTick = 0;
let looping = false;
let racing = false;
let view = { sym: 3, res: 60 };
let plugsSeen = 0;

const post = (msg, transfer) => self.postMessage(msg, transfer || []);
const yieldNow = () => new Promise((r) => setTimeout(r, 0));
const snapshot = () => post({ type: 'snapshot', ...run.snapshot(view) });

async function init() {
  sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
  const base = new URL('../../lib/go/', import.meta.url).href;
  const [module, policy] = await Promise.all([
    loadGo(base),
    fetch('policy.precompute').then((r) => {
      if (!r.ok) throw new Error(`policy.precompute: HTTP ${r.status}`);
      return r.text();
    }),
  ]);
  run = await new Run(sqlite3, module, policy).init();
  post({ type: 'ready', sqlite: sqlite3.version.libVersion, version: run.go.version, policy, schema: run.compiled.schema, distill: run.compiled.distill });
  snapshot();
}

function reportPlugs() {
  while (plugsSeen < run.plugs.length) post({ type: 'plug', plug: run.plugs[plugsSeen++] });
}

async function loop() {
  if (looping) return;
  looping = true;
  try {
    while (playing && !run.done && !racing) {
      const target = fullSpeed ? Infinity : anchorSim + ((performance.now() - anchorWall) / 1000) * SPEED;
      const want = Math.floor(target - run.t);
      if (want > 0) await run.step(Math.min(want, 1e6), 60);
      else await new Promise((r) => setTimeout(r, 16));
      reportPlugs();
      if (performance.now() - lastTick > TICK_MS || run.done) {
        lastTick = performance.now();
        snapshot();
        // Checks made after a recovery arrive with the plug records.
        for (const p of run.plugs) if (p.check && !p.checkPosted) { p.checkPosted = true; post({ type: 'plug', plug: p }); }
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
  anchorWall = performance.now();
  anchorSim = run.t;
}

async function race() {
  if (racing) return;
  const resume = playing;
  playing = false;
  while (looping) await new Promise((r) => setTimeout(r, 5));
  racing = true;
  try {
    const r = new Race(sqlite3, run.go, run.policy, run.compiled);
    post({ type: 'race', phase: 'start', ...r.progress() });
    let last = 0;
    while (!r.done) {
      r.step(25);
      if (performance.now() - last > 80) { last = performance.now(); post({ type: 'race', phase: 'run', ...r.progress() }); }
      await yieldNow();
    }
    post({ type: 'race', phase: 'compare', ...r.progress() });
    await yieldNow();
    const tables = r.compare();
    post({ type: 'race', phase: 'done', ...r.progress(), tables });
    r.close();
  } catch (e) {
    post({ type: 'error', message: `The race stopped: ${e.message || e}` });
  } finally {
    racing = false;
  }
  if (resume && !run.done) { playing = true; anchor(); loop(); }
}

self.onmessage = async (ev) => {
  const m = ev.data;
  try {
    switch (m.type) {
      case 'play':
        if (racing) break;
        if (run.done) { await run.reset(); plugsSeen = 0; snapshot(); }
        playing = true; fullSpeed = false; anchor(); loop();
        break;
      case 'pause':
        playing = false;
        snapshot();
        break;
      case 'stop':
        playing = false; fullSpeed = false;
        while (looping) await new Promise((r) => setTimeout(r, 5));
        await run.reset();
        plugsSeen = 0;
        snapshot();
        break;
      case 'review':
        playing = true; fullSpeed = true; loop();
        break;
      case 'view':
        view = { sym: m.sym, res: m.res };
        if (!playing) snapshot();
        break;
      case 'jump': {
        const j = run.jump(m.sym, m.factor);
        post({ type: 'event', t: j.t, kind: 'jump', text: `You moved ${j.symbol} ${m.factor > 1 ? 'up' : 'down'} ${Math.round(Math.abs(m.factor - 1) * 100)}%.` });
        break;
      }
      case 'plug':
        run.armPlug('you');
        if (!playing) {
          await run.checkpoint();
          reportPlugs();
          snapshot();
        }
        break;
      case 'race':
        race();
        break;
      case 'query':
        post({ type: 'query', id: m.id, result: run.query(m.sql) });
        break;
      case 'compile': {
        const t0 = performance.now();
        const res = run.go.compile(m.text, 'policy.precompute');
        post({ type: 'compiled', id: m.id, schema: res.schema, error: res.error, ms: performance.now() - t0 });
        break;
      }
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
