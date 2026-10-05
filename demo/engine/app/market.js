// The Demo 2 scenario: one simulated trading day for eight invented symbols.
//
// Everything here is plain integer and floating-point arithmetic (no Math.exp, Math.log or
// Math.pow), so every browser, Node and the Go copy in internal/market produce the very same
// trades, and a run left alone ends with the published numbers. The symbols and prices are made up.

export const OPEN = 1790602200;       // 2026-09-28 09:30:00 New York (13:30 UTC)
export const DAY = 23400;             // 09:30 to 16:00
export const SYMBOLS = ['SIM1', 'SIM2', 'SIM3', 'SIM4', 'SIM5', 'SIM6', 'SIM7', 'SIM8'];
const START_CENTS = [18740, 6425, 41280, 2310, 9860, 14530, 3175, 5290];
const RATE = [40, 34, 27, 23, 19, 15, 11, 7];              // trades per second on an average moment
const DAILY_VOL = [0.022, 0.018, 0.025, 0.03, 0.016, 0.02, 0.028, 0.024];
const SPREAD = [2, 1, 3, 1, 1, 2, 1, 1];                    // bid-ask spread in cents
export const JUMPS = [
  { sym: 3, t: 5537, factor: 1.08 },    // SIM4 +8% at 11:02:17
  { sym: 5, t: 18065, factor: 0.94 },   // SIM6 -6% at 14:31:05
];
export const HALT = { sym: 7, from: 9000, to: 9600, factor: 0.965 }; // SIM8 halted 12:00 to 12:10, reopens 3.5% lower
const SQRT3 = 1.7320508075688772;
const MOVE_SEED = 4000;                // picked for an ordinary day: no big moves without news

export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// A bell-shaped number with mean 0 and spread 1, from four uniform ones (never beyond ±3.46).
function bell(r) {
  return (r() + r() + r() + r() - 2) * SQRT3;
}

// Activity through the day: busy at the open and the close, quiet at lunch.
export function activity(t) {
  const x = (2 * t) / DAY - 1;          // -1 at the open, 1 at the close
  let a = 0.45 + 1.1 * x * x + 0.9 * x * x * x * x * x * x;
  a *= ramp(t, 1800, 3000, 1.6);        // 10:00 to 10:20, a busy stretch
  a *= ramp(t, 10800, 14400, 0.65);     // 12:30 to 13:30, calm
  a *= ramp(t, 18065, 18900, 1.35);     // after the SIM6 news
  return a;
}

// ramp scales by f between from and to, easing in and out over two minutes.
function ramp(t, from, to, f) {
  const e = 120;
  let w;
  if (t <= from - e || t >= to + e) return 1;
  if (t < from) w = (t - (from - e)) / e;
  else if (t > to) w = (to + e - t) / e;
  else w = 1;
  return 1 + (f - 1) * w;
}

function surge(s, t) {
  for (const j of JUMPS) if (j.sym === s && t >= j.t && t < j.t + 300) return 3;
  if (s === HALT.sym && t >= HALT.to && t < HALT.to + 180) return 4;
  return 1;
}

const SIZES = [[0.2, 0], [0.62, 100], [0.76, 200], [0.84, 300], [0.9, 500], [0.955, 1000], [0.985, 2000], [1, 5000]];

export class Market {
  constructor() {
    this.t = 0;
    this.mid = START_CENTS.map((c) => c + 0.5);
    this.side = SYMBOLS.map(() => 1);
    this.sigma = DAILY_VOL.map((v, s) => v / Math.sqrt(RATE[s] * DAY));
    this.rates = mulberry32(11);
    this.moves = SYMBOLS.map((_, s) => mulberry32(MOVE_SEED + s));
    this.flow = SYMBOLS.map((_, s) => mulberry32(200 + s));
    this.jumps = JUMPS.map((j) => ({ ...j }));
    this.extra = [];                    // jumps the visitor adds
    this.halted = (s, t) => s === HALT.sym && t >= HALT.from && t < HALT.to;
    this.count = new Int32Array(SYMBOLS.length);
  }

  // jump moves a symbol's price by a factor from the next second on (the visitor's button).
  jump(sym, factor) {
    this.extra.push({ sym, t: this.t, factor });
  }

  // second makes the trades of the next simulated second and returns them, symbols interleaved:
  // { ts, n, sym: Uint8Array, price: Float64Array, size: Float64Array }.
  second() {
    const t = this.t;
    const ts = OPEN + t;
    const act = activity(t);
    let total = 0;
    const counts = this.count;
    for (let s = 0; s < SYMBOLS.length; s++) {
      const u = this.rates();
      const v = this.rates();
      if (this.halted(s, t)) { counts[s] = 0; continue; }
      const lam = RATE[s] * act * surge(s, t);
      // About lam trades, spread like a Poisson count.
      const z = (u + v - 1) * 2.449489742783178;      // spread 1, from two uniforms
      let n = Math.floor(lam + z * Math.sqrt(lam) + 0.5);
      counts[s] = n < 0 ? 0 : n;
      total += counts[s];
      for (const j of this.jumps.concat(this.extra)) if (j.sym === s && j.t === t) this.mid[s] *= j.factor;
      if (s === HALT.sym && t === HALT.to) this.mid[s] *= HALT.factor;
    }
    const out = { ts, n: total, sym: new Uint8Array(total), price: new Float64Array(total), size: new Float64Array(total) };
    const left = Int32Array.from(counts);
    let k = 0;
    while (k < total) {
      for (let s = 0; s < SYMBOLS.length; s++) {
        if (left[s] === 0) continue;
        left[s]--;
        const r = this.moves[s], f = this.flow[s];
        this.mid[s] *= 1 + this.sigma[s] * bell(r);
        if (f() < 0.4) this.side[s] = -this.side[s];
        const sp = SPREAD[s];
        const bid = Math.floor(this.mid[s] - (sp - 1) / 2);
        const cents = this.side[s] > 0 ? bid + sp : bid;
        const q = f();
        let size = 0;
        for (const [p, lot] of SIZES) {
          if (q < p) { size = lot === 0 ? 1 + Math.floor(f() * 99) : lot; break; }
        }
        out.sym[k] = s;
        out.price[k] = cents / 100;
        out.size[k] = size;
        k++;
      }
    }
    this.t++;
    return out;
  }
}
