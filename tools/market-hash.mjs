// Prints a hash of the whole Demo 2 trading day as the JavaScript scenario makes it.
// internal/market/market_test.go checks that the Go copy makes the same day.
import { Market, DAY } from '../demo/engine/app/market.js';

const f = new Float64Array(1), w = new Uint32Array(f.buffer);
let h = 2166136261, n = 0;
const mix = (x) => { h = Math.imul(h ^ x, 16777619) >>> 0; };
const m = new Market();
for (let t = 0; t < DAY; t++) {
  const o = m.second();
  for (let i = 0; i < o.n; i++) {
    f[0] = o.price[i];
    mix(o.ts); mix(o.sym[i]); mix(w[0]); mix(w[1]); mix(o.size[i]);
  }
  n += o.n;
}
console.log(JSON.stringify({ trades: n, hash: h }));
