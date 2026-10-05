// Demo 6's web shop: the same kind of shop as Demo 4, over a day of history and two hours of today.
//
// History runs from 29 September 2026, 12:00 UTC, for 24 hours, with six incidents the on-call team
// labelled, two deploys that log extra debug lines and one burst of traffic from a newsletter. Today
// runs from 30 September, 12:00 UTC, for two hours: incidents of kinds seen before, one of a kind
// never seen, the on-call team labelling it, and the same kind again. Visitors can start any kind at
// any moment. Lines are drawn with plain arithmetic (no Math.exp, Math.log or Math.sin), so every
// browser and Node write the same lines.

export const HISTORY_START = 1790683200;            // 2026-09-29 12:00:00 UTC
export const HISTORY_DURATION = 86400;
export const TODAY_START = HISTORY_START + HISTORY_DURATION;
export const TODAY_DURATION = 7200;
export const BASE_RATE = 30;                          // lines a second at an average moment
export const SERVICES = ['web', 'search', 'checkout', 'payments', 'login'];
export const FORMAT = '<timestamp> <level> <service> <message>';

// The kinds of incident the shop can have. new: never seen in the history.
export const KINDS = {
  payments: { name: 'Card payments failing', minutes: 8 },
  attack: { name: 'Login attack', minutes: 6 },
  search: { name: 'Search shard lost', minutes: 7 },
  dbpool: { name: 'Database pool exhausted', minutes: 7 },
  cache: { name: 'Recommendations cache down', minutes: 6 },
  tls: { name: 'Image CDN certificate expired', minutes: 6, new: true },
  queue: { name: 'Order queue out of disk', minutes: 6, new: true },
};

const at = (day, hh, mm) => HISTORY_START - 12 * 3600 + (day - 29) * 86400 + hh * 3600 + mm * 60;

// What the on-call team wrote down about yesterday's incidents.
export const HISTORY_INCIDENTS = [
  { kind: 'payments', provider: 'northpay', at: at(29, 14, 10), minutes: 8,
    title: 'Card payments failing: northpay outage', fix: 'Moved card payments to quickcard until northpay recovered.' },
  { kind: 'attack', at: at(29, 17, 40), minutes: 6,
    title: 'Login attack from a botnet', fix: 'Turned on the per-IP sign-in limit and blocked the attacking network.' },
  { kind: 'search', at: at(29, 21, 5), minutes: 7,
    title: 'Search shard 3 lost its replica', fix: 'Restarted search-2; shard 3 rebuilt in six minutes.' },
  { kind: 'dbpool', at: at(30, 2, 30), minutes: 8,
    title: 'Checkout database pool exhausted', fix: 'Stopped a runaway report query and raised the pool from 20 to 40 connections.' },
  { kind: 'cache', at: at(30, 6, 15), minutes: 6,
    title: 'Recommendations cache down', fix: 'Restarted cache-2 and cut the recommendations timeout to 150 ms.' },
  { kind: 'payments', provider: 'quickcard', at: at(30, 9, 50), minutes: 6,
    title: 'Card payments failing: quickcard outage', fix: 'Moved card payments to northpay until quickcard recovered.' },
];

// Things in the history that are not incidents: deploys that log debug lines, and a newsletter.
export const HISTORY_EVENTS = [
  { kind: 'deploy', at: at(29, 16, 0), minutes: 40 },
  { kind: 'newsletter', at: at(29, 19, 0), minutes: 20, factor: 1.8 },
  { kind: 'deploy', at: at(30, 8, 0), minutes: 40 },
];

// Today, in seconds after TODAY_START.
export const TODAY_INCIDENTS = [
  { kind: 'payments', provider: 'northpay', at: 18 * 60 + 12, minutes: 8 },
  { kind: 'dbpool', at: 52 * 60 + 40, minutes: 7 },
  { kind: 'tls', host: 'img.cdn-a.example', at: 74 * 60 + 5, minutes: 6 },
  { kind: 'attack', at: 94 * 60 + 30, minutes: 5 },
  { kind: 'tls', host: 'img.cdn-b.example', at: 108 * 60 + 20, minutes: 5 },
];
// The on-call team writes down the new kind of incident once it is over.
export const TODAY_LABEL = { at: 82 * 60, kind: 'tls', title: 'Image CDN certificate expired',
  fix: 'Renewed the certificate on the image CDN and turned on expiry alerts.' };

// exp(0.5 z) at 512 evenly spaced quantiles of the normal distribution: the spread of latencies.
const SPREAD = [0.2125,0.2521,0.2747,0.2915,0.3051,0.3167,0.327,0.3363,0.3448,0.3526,0.36,0.3669,0.3734,0.3796,0.3856,0.3913,0.3967,0.402,0.4071,0.412,0.4168,0.4215,0.426,0.4304,0.4347,0.4389,0.4431,0.4471,0.4511,0.455,0.4588,0.4625,0.4662,0.4698,0.4734,0.4769,0.4804,0.4838,0.4872,0.4905,0.4938,0.4971,0.5003,0.5035,0.5066,0.5097,0.5128,0.5159,0.5189,0.5219,0.5248,0.5278,0.5307,0.5336,0.5364,0.5393,0.5421,0.5449,0.5477,0.5504,0.5532,0.5559,0.5586,0.5613,0.5639,0.5666,0.5692,0.5719,0.5745,0.5771,0.5796,0.5822,0.5847,0.5873,0.5898,0.5923,0.5948,0.5973,0.5998,0.6023,0.6047,0.6072,0.6096,0.6121,0.6145,0.6169,0.6193,0.6217,0.6241,0.6265,0.6288,0.6312,0.6335,0.6359,0.6382,0.6406,0.6429,0.6452,0.6475,0.6498,0.6522,0.6545,0.6567,0.659,0.6613,0.6636,0.6659,0.6681,0.6704,0.6726,0.6749,0.6771,0.6794,0.6816,0.6839,0.6861,0.6883,0.6906,0.6928,0.695,0.6972,0.6994,0.7016,0.7038,0.706,0.7082,0.7104,0.7126,0.7148,0.717,0.7192,0.7214,0.7236,0.7258,0.7279,0.7301,0.7323,0.7345,0.7367,0.7388,0.741,0.7432,0.7453,0.7475,0.7497,0.7518,0.754,0.7562,0.7583,0.7605,0.7627,0.7648,0.767,0.7691,0.7713,0.7735,0.7756,0.7778,0.7799,0.7821,0.7843,0.7864,0.7886,0.7907,0.7929,0.7951,0.7972,0.7994,0.8016,0.8037,0.8059,0.8081,0.8102,0.8124,0.8146,0.8167,0.8189,0.8211,0.8232,0.8254,0.8276,0.8298,0.8319,0.8341,0.8363,0.8385,0.8407,0.8429,0.845,0.8472,0.8494,0.8516,0.8538,0.856,0.8582,0.8604,0.8626,0.8648,0.867,0.8693,0.8715,0.8737,0.8759,0.8781,0.8804,0.8826,0.8848,0.887,0.8893,0.8915,0.8938,0.896,0.8983,0.9005,0.9028,0.905,0.9073,0.9095,0.9118,0.9141,0.9164,0.9186,0.9209,0.9232,0.9255,0.9278,0.9301,0.9324,0.9347,0.937,0.9393,0.9417,0.944,0.9463,0.9486,0.951,0.9533,0.9557,0.958,0.9604,0.9627,0.9651,0.9675,0.9698,0.9722,0.9746,0.977,0.9794,0.9818,0.9842,0.9866,0.989,0.9915,0.9939,0.9963,0.9988,1.001,1.004,1.006,1.009,1.011,1.014,1.016,1.019,1.021,1.024,1.026,1.029,1.031,1.034,1.036,1.039,1.041,1.044,1.046,1.049,1.052,1.054,1.057,1.059,1.062,1.065,1.067,1.07,1.073,1.075,1.078,1.08,1.083,1.086,1.089,1.091,1.094,1.097,1.099,1.102,1.105,1.108,1.11,1.113,1.116,1.119,1.122,1.125,1.127,1.13,1.133,1.136,1.139,1.142,1.145,1.147,1.15,1.153,1.156,1.159,1.162,1.165,1.168,1.171,1.174,1.177,1.18,1.183,1.186,1.19,1.193,1.196,1.199,1.202,1.205,1.208,1.212,1.215,1.218,1.221,1.224,1.228,1.231,1.234,1.238,1.241,1.244,1.248,1.251,1.254,1.258,1.261,1.265,1.268,1.272,1.275,1.279,1.282,1.286,1.289,1.293,1.297,1.3,1.304,1.308,1.311,1.315,1.319,1.322,1.326,1.33,1.334,1.338,1.342,1.346,1.35,1.353,1.357,1.362,1.366,1.37,1.374,1.378,1.382,1.386,1.39,1.395,1.399,1.403,1.408,1.412,1.416,1.421,1.425,1.43,1.434,1.439,1.443,1.448,1.453,1.458,1.462,1.467,1.472,1.477,1.482,1.487,1.492,1.497,1.502,1.507,1.512,1.517,1.523,1.528,1.533,1.539,1.544,1.55,1.555,1.561,1.567,1.573,1.578,1.584,1.59,1.596,1.602,1.609,1.615,1.621,1.627,1.634,1.64,1.647,1.654,1.66,1.667,1.674,1.681,1.688,1.695,1.703,1.71,1.718,1.725,1.733,1.741,1.749,1.757,1.765,1.773,1.782,1.79,1.799,1.808,1.817,1.826,1.835,1.845,1.854,1.864,1.874,1.884,1.895,1.905,1.916,1.927,1.939,1.95,1.962,1.974,1.986,1.999,2.012,2.025,2.039,2.053,2.067,2.082,2.097,2.112,2.128,2.145,2.162,2.18,2.198,2.217,2.237,2.257,2.278,2.3,2.323,2.347,2.373,2.399,2.427,2.456,2.488,2.521,2.556,2.594,2.634,2.678,2.726,2.778,2.836,2.9,2.973,3.058,3.157,3.278,3.431,3.64,3.966,4.705];

export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const ROUTES = [
  // route, share of requests, median ms, typical bytes, method
  ['/', 0.12, 22, 14000, 'GET'],
  ['/search', 0.18, 48, 22000, 'GET'],
  ['/product/:id', 0.34, 31, 18000, 'GET'],
  ['/cart', 0.12, 18, 6000, 'POST'],
  ['/checkout', 0.06, 160, 4000, 'POST'],
  ['/login', 0.06, 35, 2500, 'POST'],
  ['/api/recommendations', 0.12, 85, 9000, 'GET'],
];
const TERMS = ['rain-jacket', 'running-shoes', 'tent', 'wool-socks', 'backpack', 'headlamp', 'sleeping-bag', 'water-bottle',
  'hiking-boots', 'fleece', 'gloves', 'beanie', 'trail-map', 'stove', 'mug', 'poles', 'gaiters', 'sunglasses', 'compass',
  'first-aid-kit', 'hammock', 'rope', 'knife', 'thermos', 'sandals', 'shorts', 'base-layer', 'down-vest', 'rain-pants',
  'dry-bag', 'camp-chair', 'lantern', 'bug-spray', 'sunscreen', 'duffel', 'wallet', 'watch', 'belt', 'cap', 'scarf'];
const PROVIDERS = [['northpay', 0.5], ['quickcard', 0.3], ['banklink', 0.2]];
const LINE_KINDS = [
  ['access', 0.50], ['query', 0.12], ['cart', 0.06], ['order', 0.03], ['charge', 0.035],
  ['login', 0.05], ['loginFailed', 0.008], ['expired', 0.012], ['timeout', 0.003],
];
const KIND_TOTAL = LINE_KINDS.reduce((a, k) => a + k[1], 0);

function pick(list, u) {
  let acc = 0;
  for (const it of list) { acc += it[1]; if (u < acc) return it; }
  return list[list.length - 1];
}

// How busy the shop is at an hour of the day (UTC), relative to an average moment.
const DAY = [[0, 0.45], [3, 0.25], [6, 0.35], [9, 0.9], [12, 1.1], [15, 1.0], [18, 1.2], [21, 0.9], [24, 0.45]];
export function activity(ts) {
  const h = ((ts % 86400) + 86400) % 86400 / 3600;
  for (let i = 1; i < DAY.length; i++) {
    if (h <= DAY[i][0]) {
      const [h0, a0] = DAY[i - 1], [h1, a1] = DAY[i];
      return a0 + ((a1 - a0) * (h - h0)) / (h1 - h0);
    }
  }
  return DAY[DAY.length - 1][1];
}

const pad = (n, w = 2) => String(n).padStart(w, '0');
const money = (cents) => `${Math.floor(cents / 100)}.${pad(cents % 100)}`;

// iso writes a time in seconds and a millisecond as 2026-09-30T12:34:56.789Z.
export function iso(ts, ms) {
  const d = Math.floor(ts / 86400), s = ts % 86400;
  const day = 29 + (d - Math.floor(HISTORY_START / 86400));
  return `2026-09-${pad(day)}T${pad(Math.floor(s / 3600))}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}.${pad(ms, 3)}Z`;
}

// Shop writes the lines of one second at a time, from start for a number of seconds.
// incidents and events use absolute times in seconds (at) and last for their minutes.
export class Shop {
  constructor({ start, seconds, seed, incidents = [], events = [] }) {
    this.start = start;
    this.seconds = seconds;
    this.r = mulberry32(seed);            // what happens
    this.v = mulberry32(seed ^ 0x5bd1e995); // the values in the lines
    this.t = 0;                            // the next second to write, since start
    this.incidents = incidents.map((x) => ({ ...x }));
    this.events = events.map((x) => ({ ...x }));
    this.nOrder = 20450 + (seed % 1000);
    this.nCart = 91200 + (seed % 1000);
  }

  get ts() { return this.start + this.t; }

  // add starts an incident now (or at a given absolute time) and returns it.
  add(kind, opts = {}) {
    const x = { kind, at: this.ts, minutes: KINDS[kind].minutes, ...opts };
    if (kind === 'payments' && !x.provider) x.provider = 'northpay';
    if (kind === 'tls' && !x.host) x.host = 'img.cdn-a.example';
    this.incidents.push(x);
    return x;
  }

  spread() { return SPREAD[Math.floor(this.v() * SPREAD.length)]; }

  active(list, ts) {
    const on = {};
    for (const x of list) if (ts >= x.at && ts < x.at + x.minutes * 60) on[x.kind] = x;
    return on;
  }

  // second returns the lines of the next second, in time order, and moves on. Null at the end.
  second() {
    if (this.t >= this.seconds) return null;
    const ts = this.ts;
    this.t++;
    const r = this.r, v = this.v;
    const on = this.active(this.incidents, ts), ev = this.active(this.events, ts);
    const lam = BASE_RATE * activity(ts) * (ev.newsletter ? ev.newsletter.factor : 1);
    let n = Math.floor(lam);
    if (r() < lam - n) n++;
    const out = [];
    const line = (ms, level, service, msg) => out.push([ms, `${iso(ts, ms)} ${level.padEnd(5)} ${service} ${msg}`]);
    const uid = () => `u-${10000 + Math.floor(v() * 50000)}`;
    for (let i = 0; i < n; i++) {
      const ms = Math.floor(r() * 1000);
      const kind = pick(LINE_KINDS, r() * KIND_TOTAL)[0];
      switch (kind) {
        case 'access': {
          const [route, , median, bytes, method] = pick(ROUTES, r());
          const path = route === '/product/:id' ? `/product/${1000 + Math.floor(v() * 9000)}`
            : route === '/search' ? `/search?q=${TERMS[Math.floor(v() * TERMS.length)]}` : route;
          let took = median * this.spread();
          let status = 200;
          if (route === '/product/:id' && v() < 0.02) status = 404;
          if (route === '/checkout' && on.payments) { took *= 3; if (v() < 0.3) status = 502; }
          if (route === '/search' && on.search) status = 503;
          if (on.dbpool && (route === '/checkout' || route === '/cart' || route === '/login')) {
            took *= 8;
            line(ms, 'WARN', 'web', `slow request route=${route} ms=${took.toFixed(1)}`);
          }
          if (on.cache && route === '/api/recommendations' && v() < 0.6) {
            took *= 4;
            line(ms, 'WARN', 'web', 'cache unavailable node=cache-2 fallback=origin');
          }
          if (on.tls && route === '/product/:id' && v() < 0.3) {
            status = 502;
            line(ms, 'ERROR', 'web', `tls handshake failed host=${on.tls.host} error=certificate_expired`);
            line(ms, 'WARN', 'web', `image fetch failed host=${on.tls.host} status=502`);
          }
          const b = Math.floor(bytes * (0.5 + v()));
          line(ms, 'INFO', 'web', `${method} ${path} route=${route} status=${status} ms=${took.toFixed(1)} bytes=${b}`);
          break;
        }
        case 'query': {
          let k = 0, u = v() * 4.2785, acc = 1;
          while (u > acc && k < TERMS.length - 1) { k++; acc += 1 / (k + 1); }
          const took = Math.floor(25 * this.spread()) + 3;
          if (on.search) {
            line(ms, 'ERROR', 'search', `index shard unavailable shard=3 node=search-2 ms=${took}`);
            line(ms, 'INFO', 'search', `query q=${TERMS[k]} results=0 ms=${took}`);
          } else {
            line(ms, 'INFO', 'search', `query q=${TERMS[k]} results=${Math.floor(v() * 120)} ms=${took}`);
          }
          if (ev.deploy) {
            for (let j = 0; j < 3; j++) {
              line(ms, 'DEBUG', 'search', `ranking batch=${j + 1} candidates=${50 + Math.floor(v() * 150)} features=64 took_us=${200 + Math.floor(v() * 900)}`);
            }
          }
          break;
        }
        case 'cart':
          if (on.dbpool && v() < 0.5) line(ms, 'ERROR', 'checkout', `db pool exhausted pool=orders waiting=${5 + Math.floor(v() * 40)} timeout_ms=5000`);
          else line(ms, 'INFO', 'checkout', `cart updated cart=C-${this.nCart++} items=${1 + Math.floor(v() * 6)}`);
          break;
        case 'order': {
          const id = `A-${this.nOrder++}`;
          if (on.payments && v() < 0.4) line(ms, 'WARN', 'checkout', `order failed order=${id} reason=payment_error`);
          else if (on.dbpool && v() < 0.7) {
            line(ms, 'ERROR', 'checkout', `db pool exhausted pool=orders waiting=${5 + Math.floor(v() * 40)} timeout_ms=5000`);
            line(ms, 'WARN', 'checkout', `order failed order=${id} reason=db_timeout`);
          } else if (on.queue && v() < 0.6) {
            line(ms, 'ERROR', 'checkout', 'queue write failed topic=orders error=no_space_left_on_device');
            line(ms, 'INFO', 'checkout', `order queued for retry order=${id} attempt=${1 + Math.floor(v() * 3)}`);
          } else line(ms, 'INFO', 'checkout', `order placed order=${id} items=${1 + Math.floor(v() * 5)} total=${money(900 + Math.floor(v() * 24000))}`);
          break;
        }
        case 'charge': {
          const p = pick(PROVIDERS, v())[0];
          const amount = money(500 + Math.floor(v() * 25000));
          if (on.payments && p === on.payments.provider) line(ms, 'ERROR', 'payments', `charge failed provider=${p} result=error status=503 amount=${amount}`);
          else if (v() < 0.08) line(ms, 'WARN', 'payments', `charge declined provider=${p} result=declined code=${['51', '05', '14'][Math.floor(v() * 3)]} amount=${amount}`);
          else line(ms, 'INFO', 'payments', `charge ok provider=${p} result=ok amount=${amount} ms=${Math.floor(300 * this.spread())}`);
          break;
        }
        case 'login':
          if (on.dbpool && v() < 0.5) line(ms, 'ERROR', 'login', `db pool exhausted pool=users waiting=${5 + Math.floor(v() * 40)} timeout_ms=5000`);
          else line(ms, 'INFO', 'login', `signed in user=${uid()} method=${v() < 0.7 ? 'password' : 'sso'}`);
          break;
        case 'loginFailed':
          line(ms, 'WARN', 'login', `sign-in failed user=${uid()} reason=bad_password`);
          break;
        case 'expired':
          line(ms, 'INFO', 'login', `session expired user=${uid()}`);
          break;
        case 'timeout':
          line(ms, 'ERROR', 'web', `upstream timeout upstream=recommendations ms=${250 + Math.floor(v() * 50)}`);
          break;
      }
    }
    // A login attack adds failed sign-ins from many addresses; a cache outage adds timeouts.
    if (on.attack) {
      const extra = 9 + Math.floor(r() * 4);
      for (let i = 0; i < extra; i++) {
        const ms = Math.floor(r() * 1000);
        line(ms, 'WARN', 'login', `sign-in failed user=${uid()} reason=bad_password`);
        if (v() < 0.2) line(ms, 'WARN', 'web', `too many attempts ip=203.0.113.${1 + Math.floor(v() * 250)} window=60s`);
      }
    }
    if (on.cache && r() < 0.3 * activity(ts)) {
      line(Math.floor(r() * 1000), 'ERROR', 'web', `upstream timeout upstream=recommendations ms=${1500 + Math.floor(v() * 500)}`);
    }
    // Every service checks its own health every ten seconds.
    if (ts % 10 === 0) for (const [k, s] of SERVICES.entries()) line(100 * k, 'DEBUG', s, `health check ok ms=${1 + Math.floor(v() * 4)}`);
    out.sort((a, b) => a[0] - b[0]);
    return out.map((x) => x[1]);
  }
}

// The shop's history and today, each from its own seed.
export const historyShop = () => new Shop({ start: HISTORY_START, seconds: HISTORY_DURATION, seed: 929,
  incidents: HISTORY_INCIDENTS, events: HISTORY_EVENTS });
export const todayShop = () => new Shop({ start: TODAY_START, seconds: TODAY_DURATION, seed: 930,
  incidents: TODAY_INCIDENTS.map((x) => ({ ...x, at: TODAY_START + x.at })) });
