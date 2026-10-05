// Precomputing demos: shared helpers, charts, the SQL box and the browser compiler.

export const $ = (id) => document.getElementById(id);

export function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) if (kid != null) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  return el;
}

const SVGNS = 'http://www.w3.org/2000/svg';
export function s(tag, attrs = {}, text) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, v);
  if (text != null) el.textContent = text;
  return el;
}

export const fmt = {
  int: (n) => (n == null ? '–' : Math.round(n).toLocaleString('en-US')),
  bytes(b) {
    if (b == null) return '–';
    if (b >= 1e6) return (b / 1e6).toFixed(b >= 1e8 ? 0 : 1) + ' MB';
    if (b >= 1e3) return Math.round(b / 1e3) + ' KB';
    return b + ' B';
  },
  ms(v) {
    if (v == null) return '–';
    if (v >= 1000) return Math.round(v).toLocaleString('en-US') + ' ms';
    return (v >= 100 ? Math.round(v) : v.toFixed(1)) + ' ms';
  },
  time(ms) { // a duration in milliseconds, for timings
    if (ms < 1) return (ms * 1000).toFixed(ms < 0.01 ? 1 : 0) + ' µs';
    if (ms < 10) return ms.toFixed(1) + ' ms';
    if (ms < 1000) return Math.round(ms) + ' ms';
    return (ms / 1000).toFixed(1) + ' s';
  },
  clock(t) { // simulated seconds since start -> h:mm:ss
    t = Math.max(0, Math.floor(t));
    return `${Math.floor(t / 3600)}:${String(Math.floor(t / 60) % 60).padStart(2, '0')}:${String(t % 60).padStart(2, '0')}`;
  },
  times(x) { return x == null || !isFinite(x) ? '–' : (x >= 10 ? Math.round(x) : x.toFixed(1)) + '×'; },
  pct(x, d = 2) { return x == null ? '–' : (x * 100).toFixed(d) + '%'; },
};

// Time of day (UTC) for a simulated second, given the scenario's start in Unix seconds.
export function timeOfDay(start, t) {
  const d = new Date((start + t) * 1000);
  return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

function ticksFor(max, count = 4) {
  const step = niceMax(max / count);
  const out = [];
  for (let v = 0; v <= max + 1e-9; v += step) out.push(v);
  return out;
}

// A small line chart that follows a live series, with direct end labels and a hover readout.
export class LineChart {
  constructor(el, opts) {
    this.el = el;
    this.o = Object.assign({ height: 230, left: 52, right: 150, top: 12, bottom: 26 }, opts);
    this.points = [];
    this.hover = -1;
    this.tip = h('div', { class: 'tip', hidden: true });
    el.append(this.tip);
    el.tabIndex = 0;
    el.addEventListener('pointermove', (e) => this.onPointer(e));
    el.addEventListener('pointerleave', () => { this.hover = -1; this.render(); });
    el.addEventListener('keydown', (e) => this.onKey(e));
    el.addEventListener('blur', () => { this.hover = -1; this.render(); });
    new ResizeObserver(() => this.render()).observe(el);
  }

  // bands: optional shaded stretches of time, [{from, to, color}]
  setData(points, bands) { this.points = points; this.bands = bands || []; this.render(); }

  geom() {
    const w = Math.max(280, this.el.clientWidth);
    const narrow = w < 520;
    const right = narrow ? 12 : this.o.right;
    return { w, h: this.o.height, x0: this.o.left, x1: w - right, y0: this.o.top, y1: this.o.height - this.o.bottom, narrow };
  }

  onPointer(e) {
    if (!this.points.length) return;
    const g = this.geom();
    const r = this.el.getBoundingClientRect();
    const x = e.clientX - r.left;
    const xMin = this.o.xMin || 0;
    const t = xMin + ((x - g.x0) / (g.x1 - g.x0)) * (this.o.xMax - xMin);
    let best = 0;
    for (let i = 1; i < this.points.length; i++) if (Math.abs(this.points[i].t - t) < Math.abs(this.points[best].t - t)) best = i;
    this.hover = best;
    this.render();
  }

  onKey(e) {
    if (!this.points.length) return;
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      if (this.hover < 0) this.hover = this.points.length - 1;
      else this.hover = Math.max(0, Math.min(this.points.length - 1, this.hover + (e.key === 'ArrowRight' ? 1 : -1)));
      this.render();
    } else if (e.key === 'Escape') { this.hover = -1; this.render(); }
  }

  render() {
    const g = this.geom();
    const { series, xMax, xTicks, xLabel, yFormat } = this.o;
    const pts = this.points;
    let max = 0;
    for (const p of pts) for (const sr of series) max = Math.max(max, p[sr.key] || 0);
    const yMax = niceMax(max * 1.05);
    const xMin = this.o.xMin || 0;
    const X = (t) => g.x0 + ((t - xMin) / (xMax - xMin)) * (g.x1 - g.x0);
    const Y = (v) => g.y1 - (v / yMax) * (g.y1 - g.y0);
    const svg = s('svg', { viewBox: `0 0 ${g.w} ${g.h}`, height: g.h, role: 'img', 'aria-label': this.o.aria });
    const grid = s('g');
    for (const v of ticksFor(yMax)) {
      grid.append(s('line', { x1: g.x0, x2: g.x1, y1: Y(v), y2: Y(v), stroke: v === 0 ? 'var(--gray-2)' : 'var(--grid)', 'stroke-width': 1 }));
      grid.append(s('text', { x: g.x0 - 8, y: Y(v) + 4, 'text-anchor': 'end' }, yFormat(v)));
    }
    for (const t of xTicks) {
      grid.append(s('text', { x: X(t), y: g.h - 6, 'text-anchor': t === xMin ? 'start' : t === xMax ? 'end' : 'middle' }, xLabel(t)));
    }
    for (const b of this.bands || []) {
      grid.append(s('rect', { x: X(b.from), y: g.y0, width: Math.max(3, X(b.to) - X(b.from)), height: g.y1 - g.y0, fill: b.color, 'fill-opacity': b.opacity ?? 0.15 }));
    }
    svg.append(grid);
    for (const sr of series) {
      if (!pts.length) continue;
      const d = pts.map((p, i) => `${i ? 'L' : 'M'}${X(p.t).toFixed(1)} ${Y(p[sr.key] || 0).toFixed(1)}`).join('');
      svg.append(s('path', { d, fill: 'none', stroke: sr.color, 'stroke-width': 2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
    }
    const last = pts[pts.length - 1];
    if (last) {
      const ends = series.map((sr) => ({ sr, y: Y(last[sr.key] || 0) }));
      // Keep end labels apart; each stays next to its own line end.
      ends.sort((a, b) => a.y - b.y);
      for (const e of ends) e.ly = e.y;
      for (let i = 1; i < ends.length; i++) if (ends[i].ly - ends[i - 1].ly < 32) ends[i].ly = ends[i - 1].ly + 32;
      const overflow = ends.length ? ends[ends.length - 1].ly + 14 - g.y1 : 0;
      if (overflow > 0) for (const e of ends) e.ly -= overflow;
      for (const e of ends) {
        svg.append(s('circle', { cx: X(last.t), cy: e.y, r: 4, fill: e.sr.color, stroke: 'var(--card)', 'stroke-width': 2 }));
        if (!g.narrow) {
          const lx = X(last.t) + 10;
          svg.append(s('text', { x: lx, y: e.ly - 2, class: 'label' }, yFormat(last[e.sr.key] || 0, true)));
          svg.append(s('text', { x: lx, y: e.ly + 13 }, e.sr.name));
        }
      }
    }
    if (this.hover >= 0 && pts[this.hover]) {
      const p = pts[this.hover];
      svg.append(s('line', { x1: X(p.t), x2: X(p.t), y1: g.y0, y2: g.y1, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
      for (const sr of series) svg.append(s('circle', { cx: X(p.t), cy: Y(p[sr.key] || 0), r: 4, fill: sr.color, stroke: 'var(--card)', 'stroke-width': 2 }));
      this.tip.replaceChildren(
        h('div', { class: 't', text: (this.o.tipLabel || xLabel)(p.t) }),
        ...series.map((sr) => h('div', { class: 'r' }, h('i', { style: `border-color:${sr.color}` }), h('b', { text: yFormat(p[sr.key] || 0, true) }), h('span', { class: 'nm', text: sr.name }))));
      this.tip.hidden = false;
      const tx = X(p.t) + 14;
      this.tip.style.left = Math.min(tx, g.w - 190) + 'px';
      this.tip.style.top = g.y0 + 'px';
    } else this.tip.hidden = true;
    this.el.querySelector('svg')?.remove();
    this.el.prepend(svg);
  }
}

// One small chart per row (an endpoint), all on the same time axis.
export class Multiples {
  constructor(el, opts) {
    this.el = el;
    this.o = Object.assign({ rowHeight: 46, gap: 8, left: 132, right: 64, bottom: 24 }, opts);
    this.points = [];
    this.bands = [];
    this.hover = -1;
    this.tip = h('div', { class: 'tip', hidden: true });
    el.append(this.tip);
    el.tabIndex = 0;
    el.addEventListener('pointermove', (e) => this.onPointer(e));
    el.addEventListener('pointerleave', () => { this.hover = -1; this.render(); });
    el.addEventListener('keydown', (e) => LineChart.prototype.onKey.call(this, e));
    el.addEventListener('blur', () => { this.hover = -1; this.render(); });
    new ResizeObserver(() => this.render()).observe(el);
  }

  setData(points, bands) { this.points = points; this.bands = bands || []; this.render(); }

  geom() {
    const w = Math.max(280, this.el.clientWidth);
    const narrow = w < 460;
    const left = narrow ? 96 : this.o.left;
    const rows = this.o.rows.length;
    return { w, x0: left, x1: w - (narrow ? 44 : this.o.right), h: rows * (this.o.rowHeight + this.o.gap) + this.o.bottom, narrow };
  }

  onPointer(e) { LineChart.prototype.onPointer.call(this, e); }

  render() {
    const g = this.geom();
    const { rows, xMax, xTicks, xLabel, color, key } = this.o;
    const pts = this.points;
    const X = (t) => g.x0 + (t / xMax) * (g.x1 - g.x0);
    const svg = s('svg', { viewBox: `0 0 ${g.w} ${g.h}`, height: g.h, role: 'img', 'aria-label': this.o.aria });
    rows.forEach((name, i) => {
      const top = i * (this.o.rowHeight + this.o.gap);
      const bot = top + this.o.rowHeight;
      let max = 0;
      for (const p of pts) if (p[key][i] != null) max = Math.max(max, p[key][i]);
      const yMax = niceMax(max * 1.1);
      const Y = (v) => bot - (v / yMax) * (this.o.rowHeight - 12);
      for (const b of this.bands) if (b.row === i) {
        svg.append(s('rect', { x: X(b.from), y: top, width: Math.max(4, X(b.to) - X(b.from)), height: this.o.rowHeight, fill: 'var(--red)', 'fill-opacity': 0.15 }));
      }
      svg.append(s('line', { x1: g.x0, x2: g.x1, y1: bot, y2: bot, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
      svg.append(s('line', { x1: g.x0, x2: g.x1, y1: Y(yMax), y2: Y(yMax), stroke: 'var(--grid)', 'stroke-width': 1 }));
      const segs = [];
      let cur = '';
      pts.forEach((p) => {
        const v = p[key][i];
        if (v == null) { if (cur) segs.push(cur); cur = ''; return; }
        cur += `${cur ? 'L' : 'M'}${X(p.t - 30).toFixed(1)} ${Y(v).toFixed(1)}`;
      });
      if (cur) segs.push(cur);
      for (const d of segs) svg.append(s('path', { d, fill: 'none', stroke: color, 'stroke-width': 1.75, 'stroke-linejoin': 'round' }));
      pts.forEach((p) => {
        if (p.kept && p.kept[i] > 0) svg.append(s('rect', { x: X(p.t - 30) - 1, y: top, width: 2, height: 7, fill: 'var(--amber)' }));
      });
      const last = [...pts].reverse().find((p) => p[key][i] != null);
      svg.append(s('text', { x: 0, y: top + 18, class: 'label' }, g.narrow ? name.replace('/api/', '') : name));
      svg.append(s('text', { x: 0, y: top + 35 }, last ? `p99 ${fmt.ms(last[key][i])}` : 'p99 –'));
      svg.append(s('text', { x: g.x1 + 6, y: Y(yMax) + 4 }, `${Math.round(yMax).toLocaleString('en-US')} ms`));
      svg.append(s('text', { x: g.x1 + 6, y: bot }, '0'));
    });
    const axisY = g.h - 6;
    for (const t of xTicks) svg.append(s('text', { x: X(t), y: axisY, 'text-anchor': t === 0 ? 'start' : t === xMax ? 'end' : 'middle' }, xLabel(t)));
    if (this.hover >= 0 && pts[this.hover]) {
      const p = pts[this.hover];
      svg.append(s('line', { x1: X(p.t - 30), x2: X(p.t - 30), y1: 0, y2: g.h - this.o.bottom, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
      this.tip.replaceChildren(
        h('div', { class: 't', text: `p99 for the minute ${xLabel(p.t - 60)} to ${xLabel(p.t)}` }),
        ...rows.map((name, i) => h('div', { class: 'r' }, h('i', { style: `border-color:${color}` }),
          h('b', { text: fmt.ms(p[key][i]) }), h('span', { class: 'nm', text: name + (p.kept && p.kept[i] ? ` · ${p.kept[i]} kept whole` : '') }))));
      this.tip.hidden = false;
      const tx = X(p.t - 30) + 14;
      this.tip.style.left = Math.max(0, Math.min(tx, g.w - 250)) + 'px';
      this.tip.style.top = '0px';
    } else this.tip.hidden = true;
    this.el.querySelector('svg')?.remove();
    this.el.prepend(svg);
  }
}

// niceStep is a round step close to span / count, for axes that do not start at zero.
function niceStep(span, count = 4) {
  if (!(span > 0)) return 1;
  const raw = span / count;
  const p = Math.pow(10, Math.floor(Math.log10(raw)));
  for (const m of [1, 2, 2.5, 5, 10]) if (raw <= m * p) return m * p;
  return 10 * p;
}

// Candles with volume below: up candles hollow teal, down candles solid red, a mark above any
// candle whose window counted an unusual step. Candles: {w, o, h, l, c, v, vwap, n, an}.
export class CandleChart {
  constructor(el, opts) {
    this.el = el;
    this.o = Object.assign({ height: 320, left: 58, right: 70, top: 14, bottom: 26, volH: 58, gap: 12 }, opts);
    this.candles = [];
    this.points = this.candles; // for the keyboard, shared with LineChart
    this.domain = null;
    this.hover = -1;
    this.tip = h('div', { class: 'tip', hidden: true });
    el.append(this.tip);
    el.tabIndex = 0;
    el.addEventListener('pointermove', (e) => this.onPointer(e));
    el.addEventListener('pointerleave', () => { this.hover = -1; this.render(); });
    el.addEventListener('keydown', (e) => LineChart.prototype.onKey.call(this, e));
    el.addEventListener('blur', () => { this.hover = -1; this.render(); });
    new ResizeObserver(() => this.render()).observe(el);
  }

  // domain: {from, to, res, label(w), ticks: [w...], title}
  setData(candles, domain) {
    if (this.domain && domain && (this.domain.res !== domain.res || this.domain.from !== domain.from)) this.hover = -1;
    this.candles = candles;
    this.points = candles;
    this.domain = domain;
    this.render();
  }

  geom() {
    const w = Math.max(280, this.el.clientWidth);
    const narrow = w < 520;
    const o = this.o;
    const x0 = narrow ? 46 : o.left, x1 = w - (narrow ? 12 : o.right);
    const pBot = o.height - o.bottom - o.volH - o.gap;
    return { w, x0, x1, pTop: o.top, pBot, vTop: pBot + o.gap, vBot: o.height - o.bottom, narrow };
  }

  X(g, w) {
    const d = this.domain;
    const slots = Math.max(1, (d.to - d.from) / d.res);
    return g.x0 + (((w - d.from) / d.res + 0.5) * (g.x1 - g.x0)) / slots;
  }

  onPointer(e) {
    if (!this.candles.length || !this.domain) return;
    const g = this.geom();
    const x = e.clientX - this.el.getBoundingClientRect().left;
    let best = 0;
    for (let i = 1; i < this.candles.length; i++) if (Math.abs(this.X(g, this.candles[i].w) - x) < Math.abs(this.X(g, this.candles[best].w) - x)) best = i;
    this.hover = best;
    this.render();
  }

  render() {
    const g = this.geom();
    const d = this.domain;
    const cs = this.candles;
    const svg = s('svg', { viewBox: `0 0 ${g.w} ${this.o.height}`, height: this.o.height, role: 'img', 'aria-label': this.o.aria });
    if (!d) { this.el.querySelector('svg')?.remove(); this.el.prepend(svg); return; }
    let lo = Infinity, hi = -Infinity, vmax = 0;
    for (const c of cs) { if (c.l < lo) lo = c.l; if (c.h > hi) hi = c.h; if (c.v > vmax) vmax = c.v; }
    if (!cs.length) { lo = 0; hi = 1; }
    const pad = Math.max((hi - lo) * 0.08, hi * 0.0005);
    lo -= pad; hi += pad;
    const step = niceStep(hi - lo, g.narrow ? 3 : 5);
    const Y = (v) => g.pBot - ((v - lo) / (hi - lo)) * (g.pBot - g.pTop);
    const V = (v) => g.vBot - (vmax ? (v / vmax) * (g.vBot - g.vTop) : 0);
    const decimals = step < 0.1 ? 2 : step < 1 ? 1 : 0;
    const grid = s('g');
    for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) {
      grid.append(s('line', { x1: g.x0, x2: g.x1, y1: Y(v), y2: Y(v), stroke: 'var(--grid)', 'stroke-width': 1 }));
      grid.append(s('text', { x: g.x0 - 8, y: Y(v) + 4, 'text-anchor': 'end' }, v.toFixed(decimals)));
    }
    grid.append(s('line', { x1: g.x0, x2: g.x1, y1: g.vBot, y2: g.vBot, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
    grid.append(s('text', { x: g.x0 - 8, y: g.vTop + 10, 'text-anchor': 'end' }, 'vol'));
    // Time labels, thinned so that they never run into each other on a narrow screen.
    let lastX = -Infinity;
    const tickX = (t) => this.X(g, t) - (g.x1 - g.x0) / Math.max(1, (d.to - d.from) / d.res) / 2;
    const ticks = d.ticks.filter((t) => { const x = tickX(t); if (x - lastX < 46) return false; lastX = x; return true; });
    if (ticks.length > 1 && tickX(ticks[ticks.length - 1]) > g.x1 - 20 && tickX(ticks[ticks.length - 1]) - tickX(ticks[ticks.length - 2]) < 60) ticks.splice(ticks.length - 2, 1);
    for (const t of ticks) {
      grid.append(s('text', { x: tickX(t), y: this.o.height - 6, 'text-anchor': t === d.from ? 'start' : t >= d.to ? 'end' : 'middle' }, d.label(t)));
    }
    svg.append(grid);
    const slotW = (g.x1 - g.x0) / Math.max(1, (d.to - d.from) / d.res);
    const bodyW = Math.max(1, Math.min(14, slotW * 0.7));
    const up = 'var(--teal)', down = 'var(--red)';
    const body = s('g');
    for (const c of cs) {
      const x = this.X(g, c.w);
      const color = c.c >= c.o ? up : down;
      body.append(s('rect', { x: x - Math.max(0.5, bodyW / 2), y: V(c.v), width: Math.max(1, bodyW), height: Math.max(0, g.vBot - V(c.v)), fill: 'var(--gray-2)', 'fill-opacity': 0.45 }));
      body.append(s('line', { x1: x, x2: x, y1: Y(c.h), y2: Y(c.l), stroke: color, 'stroke-width': slotW < 2.5 ? Math.max(0.8, slotW * 0.8) : 1.2 }));
      if (slotW >= 2.5) {
        const top = Y(Math.max(c.o, c.c)), bot = Y(Math.min(c.o, c.c));
        body.append(s('rect', { x: x - bodyW / 2, y: top, width: bodyW, height: Math.max(1, bot - top), fill: c.c >= c.o ? 'var(--card)' : down, stroke: color, 'stroke-width': 1.2 }));
      }
      if (c.an > 0) body.append(s('path', { d: `M${x - 5} ${Y(c.h) - 12}h10l-5 7z`, fill: 'var(--amber)' }));
    }
    svg.append(body);
    const last = cs[cs.length - 1];
    if (last && !g.narrow) {
      const y = Y(last.c);
      svg.append(s('line', { x1: g.x0, x2: g.x1, y1: y, y2: y, stroke: last.c >= last.o ? up : down, 'stroke-width': 1, 'stroke-dasharray': '3 3', opacity: 0.6 }));
      svg.append(s('rect', { x: g.x1 + 4, y: y - 10, width: this.o.right - 6, height: 20, rx: 3, fill: last.c >= last.o ? up : down }));
      svg.append(s('text', { x: g.x1 + 4 + (this.o.right - 6) / 2, y: y + 4, 'text-anchor': 'middle', fill: '#fff', class: 'label on' }, last.c.toFixed(2)));
    }
    if (this.hover >= 0 && cs[this.hover]) {
      const c = cs[this.hover];
      const x = this.X(g, c.w);
      svg.append(s('line', { x1: x, x2: x, y1: g.pTop, y2: g.vBot, stroke: 'var(--gray-2)', 'stroke-width': 1 }));
      const chg = c.o ? (c.c / c.o - 1) : 0;
      this.tip.replaceChildren(
        h('div', { class: 't', text: d.title(c.w) }),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Open' }), h('b', { text: c.o.toFixed(2) })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'High' }), h('b', { text: c.h.toFixed(2) })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Low' }), h('b', { text: c.l.toFixed(2) })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Close' }), h('b', { text: `${c.c.toFixed(2)} (${chg >= 0 ? '+' : ''}${(chg * 100).toFixed(2)}%)` })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'VWAP' }), h('b', { text: c.vwap.toFixed(3) })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Volume' }), h('b', { text: fmt.int(c.v) })),
        h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Trades' }), h('b', { text: fmt.int(c.n) })),
        c.an > 0 ? h('div', { class: 'r' }, h('span', { class: 'nm', text: 'Unusual steps' }), h('b', { text: String(c.an) })) : null);
      this.tip.hidden = false;
      const tx = x + 14;
      this.tip.style.left = (tx + 200 > g.w ? Math.max(0, x - 214) : tx) + 'px';
      this.tip.style.top = g.pTop + 'px';
    } else this.tip.hidden = true;
    this.el.querySelector('svg')?.remove();
    this.el.prepend(svg);
  }
}

// Renders the result of a read in the SQL box.
export function renderResult(out, res) {
  out.replaceChildren();
  if (res.error) { out.append(h('div', { class: 'err', text: res.error })); return; }
  const shown = res.rows.length;
  out.append(h('div', { class: 'meta', text: `${fmt.int(res.total)} row${res.total === 1 ? '' : 's'} in ${fmt.time(res.ms)}${res.total > shown ? `, first ${shown} shown` : ''}` }));
  const table = h('table', {},
    h('thead', {}, h('tr', {}, res.columns.map((c) => h('th', { text: c })))),
    h('tbody', {}, res.rows.map((r) => h('tr', {}, r.map((v) => {
      const isNum = typeof v === 'number' || typeof v === 'bigint';
      const text = v == null ? 'NULL' : typeof v === 'number' && !Number.isInteger(v) ? String(Number(v.toPrecision(7))) : String(v);
      return h('td', { class: isNum ? 'num' : null, text });
    })))));
  out.append(h('div', { class: 'table-wrap' }, table));
}

// Plain highlighting for policy and SQL text: comments quiet, keywords strong. Text is escaped first.
export function highlight(pre, text, lang) {
  pre.replaceChildren();
  const kw = lang === 'policy'
    ? /\b(stream|exact|id|refuse|repeats|key|value|derive|late|period|close|raw|until|closed|keep|rollup|quantiles|samples|per|anomalies|log|change|precompute|quota|by|memory|warmup|quantile|accuracy|forever|text|real|integer)\b/g
    : /\b(CREATE|TABLE|VIEW|TRIGGER|INDEX|IF|NOT|EXISTS|INSTEAD|OF|INSERT|INTO|ON|BEGIN|END|SELECT|FROM|WHERE|AND|OR|VALUES|CONFLICT|DO|UPDATE|SET|CASE|WHEN|THEN|ELSE|WITH|AS|OVER|PARTITION|BY|ORDER|ROWS|UNBOUNDED|PRECEDING|GROUP|PRIMARY|KEY|WITHOUT|ROWID|NULL|DELETE|REPLACE|CHECK|RAISE|ABORT|IGNORE|INTEGER|REAL|TEXT|LIMIT|JOIN|USING|CAST|REFERENCES)\b/g;
  const comment = lang === 'policy' ? '#' : '--';
  for (const line of text.split('\n')) {
    const at = line.indexOf(comment);
    const code = at >= 0 ? line.slice(0, at) : line;
    let last = 0;
    for (const m of code.matchAll(kw)) {
      if (m.index > last) pre.append(code.slice(last, m.index));
      pre.append(h('span', { class: 'k', text: m[0] }));
      last = m.index + m[0].length;
    }
    if (last < code.length) pre.append(code.slice(last));
    if (at >= 0) pre.append(h('span', { class: 'c', text: line.slice(at) }));
    pre.append('\n');
  }
}

// The Precomputing compiler, built for the browser (with the Engine, in one file). Loaded only when someone opens it.
let compilerPromise = null;
export function loadCompiler(base) {
  compilerPromise ??= new Promise((resolve, reject) => {
    const script = document.createElement('script');
    script.src = base + 'wasm_exec.js';
    script.onerror = () => reject(new Error('The compiler could not be loaded.'));
    script.onload = async () => {
      try {
        const go = new globalThis.Go();
        const bytes = await fetch(base + 'precomputing.wasm').then((r) => {
          if (!r.ok) throw new Error(`precomputing.wasm: HTTP ${r.status}`);
          return r.arrayBuffer();
        });
        const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
        go.run(instance);
        resolve(globalThis.precomputingCompile);
      } catch (e) { reject(e); }
    };
    document.head.append(script);
  });
  return compilerPromise;
}

export const isEmbedded = () => window.self !== window.top;
