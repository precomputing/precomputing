// The Demo 3 scenario: one month of an invented AI writing product's usage, September 2026.
//
// Six invented customers on three plans call three models through two gateways. Requests are
// drawn with plain arithmetic (no Math.exp or Math.log), so every browser and Node produce the
// same month. The rates are examples, not anyone's price list.

export const START = 1788220800;              // 2026-09-01 00:00 UTC
export const MONTH_END = 1790812800;          // 2026-10-01 00:00 UTC
export const CLOSE = MONTH_END + 86400;       // the month closes a day after it ends
export const END = CLOSE + 6 * 3600;          // the run ends at 2026-10-02 06:00 UTC
export const DISPUTE = 90 * 86400;            // raw requests stay 90 days after the close
export const MINUTE = 60;

export const MODELS = ['small', 'medium', 'large'];
const MEDIAN_IN = [600, 1200, 2500];          // tokens
const MEDIAN_OUT = [250, 500, 900];

// Example rates, in billionths of a dollar per token: [input, output].
// Small is $0.30 and $1.20 per million tokens, medium $2.50 and $10, large $12 and $48.
export const LIST = { small: [300, 1200], medium: [2500, 10000], large: [12000, 48000] };
// What the product pays the model provider, per token.
export const COST = { small: [150, 600], medium: [1250, 5000], large: [6000, 24000] };

// Plans: a monthly token quota (none for Enterprise) and a discount on the list price, in percent.
export const PLANS = {
  free: { name: 'Free', quota: 5000000, discountPct: 100 },       // pays nothing
  pro: { name: 'Pro', quota: 300000000, discountPct: 0 },
  enterprise: { name: 'Enterprise', quota: null, discountPct: 25 },
};

// rate: requests an hour at an average moment; mix: share of small, medium, large.
export const CUSTOMERS = [
  { id: 'juniper', name: 'Juniper Bakery Blog', plan: 'free', gateway: 'eu', rate: 7, mix: [0.9, 0.1, 0] },
  { id: 'northgate', name: 'Northgate Tutors', plan: 'free', gateway: 'eu', rate: 2, mix: [1, 0, 0] },
  { id: 'harbor', name: 'Harbor Legal Drafts', plan: 'pro', gateway: 'eu', rate: 55, mix: [0, 0.4, 0.6] },
  { id: 'quarry', name: 'Quarry Road Media', plan: 'pro', gateway: 'us', rate: 130, mix: [0.3, 0.7, 0] },
  { id: 'maple', name: 'Maple Grove Travel', plan: 'pro', gateway: 'us', rate: 85, mix: [0.8, 0.2, 0] },
  { id: 'sundial', name: 'Sundial Research', plan: 'enterprise', gateway: 'us', rate: 210, mix: [0.4, 0.4, 0.2] },
];
export const GATEWAYS = ['eu', 'us'];

// The eu gateway loses its link to the meter for six hours and then delivers what it held.
export const OUTAGE = { gateway: 'eu', from: START + 14 * 86400 + 8 * 3600, to: START + 14 * 86400 + 14 * 3600 };
// Around midnight at the month's end the eu gateway is slow again: September requests arrive in October.
export const MONTH_END_DELAY = { gateway: 'eu', from: MONTH_END - 3600, to: MONTH_END + 3600 };
// A queue stuck in the us gateway is emptied after the close: its September requests are refused.
export const STUCK = { gateway: 'us', from: MONTH_END - 6 * 3600, to: MONTH_END - 5 * 3600, at: CLOSE + 5 * 3600 };
export const RETRY_SHARE = 0.02;

// exp(0.7 z) at 512 evenly spaced quantiles of the normal distribution: the spread of request sizes.
const SPREAD = [0.1144,0.1453,0.1638,0.178,0.1897,0.2,0.2091,0.2175,0.2252,0.2324,0.2392,0.2457,0.2518,0.2577,0.2634,0.2688,0.2741,0.2792,0.2842,0.289,0.2937,0.2983,0.3028,0.3072,0.3115,0.3158,0.3199,0.324,0.3281,0.332,0.3359,0.3398,0.3436,0.3473,0.351,0.3547,0.3583,0.3619,0.3654,0.3689,0.3724,0.3758,0.3792,0.3826,0.386,0.3893,0.3926,0.3959,0.3991,0.4023,0.4055,0.4087,0.4119,0.415,0.4181,0.4212,0.4243,0.4274,0.4304,0.4335,0.4365,0.4395,0.4425,0.4455,0.4485,0.4514,0.4544,0.4573,0.4602,0.4631,0.466,0.4689,0.4718,0.4747,0.4775,0.4804,0.4832,0.4861,0.4889,0.4917,0.4945,0.4973,0.5001,0.5029,0.5057,0.5085,0.5113,0.5141,0.5168,0.5196,0.5223,0.5251,0.5278,0.5306,0.5333,0.536,0.5388,0.5415,0.5442,0.5469,0.5497,0.5524,0.5551,0.5578,0.5605,0.5632,0.5659,0.5686,0.5713,0.574,0.5767,0.5794,0.5821,0.5847,0.5874,0.5901,0.5928,0.5955,0.5982,0.6009,0.6035,0.6062,0.6089,0.6116,0.6143,0.617,0.6196,0.6223,0.625,0.6277,0.6304,0.6331,0.6357,0.6384,0.6411,0.6438,0.6465,0.6492,0.6519,0.6546,0.6573,0.66,0.6627,0.6654,0.6681,0.6708,0.6735,0.6762,0.6789,0.6816,0.6843,0.687,0.6898,0.6925,0.6952,0.6979,0.7007,0.7034,0.7061,0.7089,0.7116,0.7144,0.7171,0.7199,0.7226,0.7254,0.7281,0.7309,0.7337,0.7365,0.7392,0.742,0.7448,0.7476,0.7504,0.7532,0.756,0.7588,0.7616,0.7644,0.7673,0.7701,0.7729,0.7758,0.7786,0.7814,0.7843,0.7872,0.79,0.7929,0.7958,0.7986,0.8015,0.8044,0.8073,0.8102,0.8131,0.816,0.819,0.8219,0.8248,0.8277,0.8307,0.8336,0.8366,0.8396,0.8425,0.8455,0.8485,0.8515,0.8545,0.8575,0.8605,0.8635,0.8666,0.8696,0.8726,0.8757,0.8788,0.8818,0.8849,0.888,0.8911,0.8942,0.8973,0.9004,0.9035,0.9067,0.9098,0.913,0.9161,0.9193,0.9225,0.9256,0.9288,0.932,0.9353,0.9385,0.9417,0.945,0.9482,0.9515,0.9548,0.958,0.9613,0.9646,0.968,0.9713,0.9746,0.978,0.9813,0.9847,0.9881,0.9915,0.9949,0.9983,1.002,1.005,1.009,1.012,1.016,1.019,1.023,1.026,1.03,1.033,1.037,1.04,1.044,1.047,1.051,1.055,1.058,1.062,1.066,1.069,1.073,1.077,1.08,1.084,1.088,1.092,1.095,1.099,1.103,1.107,1.111,1.114,1.118,1.122,1.126,1.13,1.134,1.138,1.142,1.146,1.15,1.154,1.158,1.162,1.166,1.17,1.174,1.179,1.183,1.187,1.191,1.195,1.2,1.204,1.208,1.212,1.217,1.221,1.225,1.23,1.234,1.239,1.243,1.248,1.252,1.257,1.261,1.266,1.27,1.275,1.28,1.284,1.289,1.294,1.299,1.303,1.308,1.313,1.318,1.323,1.328,1.333,1.338,1.343,1.348,1.353,1.358,1.363,1.368,1.373,1.379,1.384,1.389,1.394,1.4,1.405,1.411,1.416,1.422,1.427,1.433,1.438,1.444,1.45,1.456,1.461,1.467,1.473,1.479,1.485,1.491,1.497,1.503,1.509,1.515,1.521,1.528,1.534,1.54,1.547,1.553,1.56,1.566,1.573,1.58,1.586,1.593,1.6,1.607,1.614,1.621,1.628,1.635,1.642,1.65,1.657,1.664,1.672,1.679,1.687,1.695,1.702,1.71,1.718,1.726,1.734,1.742,1.75,1.759,1.767,1.776,1.784,1.793,1.802,1.81,1.819,1.828,1.837,1.847,1.856,1.866,1.875,1.885,1.895,1.904,1.914,1.925,1.935,1.945,1.956,1.967,1.977,1.988,1.999,2.011,2.022,2.034,2.045,2.057,2.069,2.082,2.094,2.107,2.12,2.133,2.146,2.159,2.173,2.187,2.201,2.215,2.23,2.245,2.26,2.275,2.291,2.307,2.323,2.34,2.357,2.374,2.392,2.41,2.428,2.447,2.466,2.486,2.506,2.526,2.547,2.569,2.591,2.614,2.637,2.661,2.685,2.711,2.737,2.763,2.791,2.819,2.849,2.879,2.911,2.943,2.977,3.012,3.048,3.086,3.126,3.167,3.21,3.255,3.302,3.352,3.405,3.46,3.519,3.582,3.649,3.72,3.797,3.88,3.971,4.071,4.18,4.302,4.44,4.598,4.782,5.001,5.27,5.618,6.103,6.882,8.742];

export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function spread(u) {
  return SPREAD[Math.floor(u * SPREAD.length)];
}

// How busy an hour is, relative to an average one: a working-day curve peaking at 14:00 UTC,
// quieter at weekends, averaging 1 over a week. 2026-09-01 is a Tuesday.
export function activity(ts) {
  const h = Math.floor((ts % 86400) / 3600);
  const d = Math.floor((ts - START) / 86400);
  const weekday = (d + 2) % 7;                 // 0 is Sunday
  const peak = 1 - Math.abs(h - 14) / 12;      // 0 at 02:00, 1 at 14:00
  const day = 0.25 + 1.5 * peak * peak;
  return weekday === 0 || weekday === 6 ? day * 0.64 : day * 1.58;
}

// Draws gives the requests of one customer in one minute, with sizes and retries.
export class Draws {
  constructor() {
    this.count = CUSTOMERS.map((_, i) => mulberry32(301 + i));
    this.size = CUSTOMERS.map((_, i) => mulberry32(401 + i));
    this.retry = mulberry32(501);
    this.n = 0;
  }

  // minute returns the requests customer c makes in the minute starting at t0:
  // [{ts, model, input, output, retryAfter (0 for none), retryOther}]
  minute(c, t0, retryShare) {
    const cu = CUSTOMERS[c];
    const lam = (cu.rate / 60) * activity(t0);
    const r = this.count[c];
    // A count with that mean, as sixteen tries with equal chances.
    let k = 0;
    for (let i = 0; i < 16; i++) if (r() < lam / 16) k++;
    const out = [];
    for (let i = 0; i < k; i++) {
      const s = this.size[c];
      const u = s();
      const m = u < cu.mix[0] ? 0 : u < cu.mix[0] + cu.mix[1] ? 1 : 2;
      const ts = t0 + Math.floor(s() * MINUTE);
      const input = Math.max(1, Math.floor(MEDIAN_IN[m] * spread(s())));
      const output = Math.max(1, Math.floor(MEDIAN_OUT[m] * spread(s())));
      const q = this.retry();
      const retryAfter = q < retryShare ? 1 + Math.floor(this.retry() * 30) : 0;
      const retryOther = retryAfter > 0 && this.retry() < 0.3;
      out.push({ ts, model: m, input, output, retryAfter, retryOther });
    }
    out.sort((a, b) => a.ts - b.ts);
    return out;
  }
}
