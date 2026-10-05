package engine

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/policy"
)

// stream holds the in-memory state of one stream. Every update below follows the compiled trigger
// step by step, with the same arithmetic in the same order, so the rows it writes are the rows the
// trigger would have written.
type stream struct {
	e       *Engine
	idx     int
	pol     *policy.Stream
	lay     *compile.StreamLayout
	keyText []bool // per key: text (true) or integer
	valInt  []bool // per value: integer (true) or real
	nvals   int
	line    bool // a stream from logs: raw events, samples and anomalies keep the line
	derived []expr

	keyIndex map[string]int32
	keys     [][]any // key id -> tuple of string and int64

	rollups []*rollup
	anom    *anomalyRule
	sampleR *rollup
	sampleN int64
	groups  []*group

	base     []*baseRow // by key id
	baseSeen []bool     // the file was asked for this key's baseline

	raw   rawBuf
	anoms anomBuf

	// Exact streams: identifiers seen (to refuse repeats), the newest event's time, and counts of
	// refused events. All three are written at checkpoints like everything else.
	ids          map[any]int64 // identifier -> event time
	newIDs       []any
	clock        int64
	clockSet     bool
	clockDirty   bool
	refused      map[refusedKey]*refusedRow
	refusedDirty []*refusedRow

	nv []num
	nf []float64
}

type refusedKey struct {
	reason string
	key    int32
}

type refusedRow struct {
	reason string
	key    int32
	n      int64
	dirty  bool
}

type rollup struct {
	res      int64
	keep     int64
	quantNum []int    // number index of each quantile sketch this rollup keeps
	quantSk  []string // its sketch table
	samples  bool
	cur      []*window // by key id: the window last touched
	water    []int64   // by key id: newest window start that may be in the file
	wins     map[winKey]*window
	dirty    []*window
}

type winKey struct {
	w   int64
	key int32
}

type window struct {
	w                      int64
	key                    int32
	n, firstTS, lastTS, an int64
	st                     []float64 // per number: sum, sumsq, min, max, first, last
	cells                  []map[int64]*cell
	dirtyCells             [][]*cell
	samp                   []*sampleSlot
	dirty                  bool
}

type cell struct {
	b, n  int64
	dirty bool
}

type sampleSlot struct {
	ts    int64
	vals  []float64
	line  string
	dirty bool
}

type anomalyRule struct {
	num    int
	log    bool
	change bool
	z2     float64
	warm   int64
	keep   int64
	per    *rollup
	alpha  float64
	clip   float64
	huber  float64
}

type baseRow struct {
	n     int64
	m, v  float64
	prev  float64
	dirty bool
}

type rawBuf struct {
	ts   []int64
	id   []any
	key  []int32
	vals []float64
	line []string
}

type anomBuf struct {
	ts   []int64
	key  []int32
	vals []float64
	line []string
	z    []float64
}

type group struct {
	s      *stream
	lay    *compile.GroupLayout
	byIdx  []int
	goFmt  string
	sumN   []int
	minN   []int
	maxN   []int
	fstN   []int
	lstN   []int
	quantN int
	proj   []int32 // stream key id -> group key id, -1 until known
	gindex map[string]int32
	gkeys  [][]any
	cur    []*groupRow // by group key id
	rows   map[groupKey]*groupRow
	dirty  []*groupRow
}

type groupKey struct {
	key    int32
	period string
}

type groupRow struct {
	key          int32
	period       string
	pStart, pEnd int64
	n            int64
	vals         []float64 // sums, then minimums, maximums, firsts and lasts
	firstTS      int64
	lastTS       int64
	cells        map[int64]*cell
	dirtyCells   []*cell
	dirty        bool
}

func newStream(e *Engine, idx int, lay *compile.StreamLayout) *stream {
	p := lay.Stream
	s := &stream{e: e, idx: idx, pol: p, lay: lay, nvals: len(p.Values), line: lay.Line, keyIndex: map[string]int32{}}
	if p.Exact {
		s.ids = map[any]int64{}
		s.refused = map[refusedKey]*refusedRow{}
	}
	for _, k := range p.Keys {
		s.keyText = append(s.keyText, k.Type != "integer")
	}
	index := map[string]int{}
	for i, v := range p.Values {
		s.valInt = append(s.valInt, v.Type == "integer")
		index[v.Name] = i
	}
	for i, d := range p.Derived {
		s.derived = append(s.derived, compileExpr(d.Expr, index))
		index[d.Name] = len(p.Values) + i
	}
	numIdx := func(name string) int { return index[name] }
	for _, r := range p.Rollups {
		ru := &rollup{res: r.Res, keep: r.Keep, wins: map[winKey]*window{}}
		for _, q := range r.Quantiles {
			ru.quantNum = append(ru.quantNum, numIdx(q))
			for _, sk := range lay.Sketches {
				if sk.Field == q {
					ru.quantSk = append(ru.quantSk, sk.Table)
				}
			}
		}
		if p.Samples != nil && p.Samples.Per == r.Res {
			ru.samples = true
			s.sampleR = ru
			s.sampleN = int64(p.Samples.N)
		}
		s.rollups = append(s.rollups, ru)
	}
	if a := p.Anomalies; a != nil {
		ar := &anomalyRule{num: numIdx(a.Field), log: a.Log, change: a.Change, z2: lay.Z2,
			warm: int64(a.Warmup), keep: int64(a.Keep), alpha: lay.Alpha, clip: lay.Clip, huber: lay.Huber}
		for _, r := range s.rollups {
			if r.res == a.Per {
				ar.per = r
			}
		}
		s.anom = ar
	}
	for _, gl := range lay.Groups {
		g := &group{s: s, lay: gl, gindex: map[string]int32{}, rows: map[groupKey]*groupRow{}}
		for _, b := range gl.By {
			for i, k := range p.Keys {
				if k.Name == b {
					g.byIdx = append(g.byIdx, i)
				}
			}
		}
		_, g.goFmt = compile.PeriodFormat(gl.Per)
		for _, f := range gl.Sum {
			g.sumN = append(g.sumN, numIdx(f))
		}
		for _, f := range gl.Min {
			g.minN = append(g.minN, numIdx(f))
		}
		for _, f := range gl.Max {
			g.maxN = append(g.maxN, numIdx(f))
		}
		for _, f := range gl.First {
			g.fstN = append(g.fstN, numIdx(f))
		}
		for _, f := range gl.Last {
			g.lstN = append(g.lstN, numIdx(f))
		}
		if gl.Quant {
			g.quantN = numIdx(gl.Field)
		}
		s.groups = append(s.groups, g)
	}
	return s
}

// keyString encodes a key tuple for the intern table.
func (s *stream) keyString(tuple []any) (string, []any, error) {
	if len(tuple) != len(s.keyText) {
		return "", nil, fmt.Errorf("stream %s has %d keys, got %d", s.pol.Name, len(s.keyText), len(tuple))
	}
	var b []byte
	norm := make([]any, len(tuple))
	for i, v := range tuple {
		name := s.pol.Keys[i].Name
		if s.keyText[i] {
			str, ok := v.(string)
			if !ok {
				return "", nil, fmt.Errorf("%s: key %s must be text", s.pol.Name, name)
			}
			b = binary.AppendUvarint(append(b, 't'), uint64(len(str)))
			b = append(b, str...)
			norm[i] = str
			continue
		}
		var n int64
		switch x := v.(type) {
		case int64:
			n = x
		case int:
			n = int64(x)
		case float64:
			if x != math.Trunc(x) || math.Abs(x) > 1<<53 {
				return "", nil, fmt.Errorf("%s: key %s must be a whole number", s.pol.Name, name)
			}
			n = int64(x)
		default:
			return "", nil, fmt.Errorf("%s: key %s must be a whole number", s.pol.Name, name)
		}
		b = binary.BigEndian.AppendUint64(append(b, 'i'), uint64(n))
		norm[i] = n
	}
	return string(b), norm, nil
}

// intern returns the id of a key tuple, adding it if it is new.
func (s *stream) intern(tuple []any) (int32, error) {
	k, norm, err := s.keyString(tuple)
	if err != nil {
		return 0, err
	}
	if id, ok := s.keyIndex[k]; ok {
		return id, nil
	}
	id := int32(len(s.keys))
	s.keyIndex[k] = id
	s.keys = append(s.keys, norm)
	s.base = append(s.base, nil)
	s.baseSeen = append(s.baseSeen, false)
	for _, r := range s.rollups {
		r.cur = append(r.cur, nil)
		r.water = append(r.water, math.MinInt64)
	}
	for _, g := range s.groups {
		g.proj = append(g.proj, -1)
	}
	return id, nil
}

// PeriodEnd is the end of the period that ts falls in, as the compiled SQL computes it.
func PeriodEnd(per string, ts int64) int64 {
	switch per {
	case "hour":
		return (ts/3600 + 1) * 3600
	case "day":
		return (ts/86400 + 1) * 86400
	}
	t := time.Unix(ts, 0).UTC()
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()
}

// PeriodStart is the start of the period that ts falls in, as the compiled SQL computes it.
func PeriodStart(per string, ts int64) int64 {
	switch per {
	case "hour":
		return ts / 3600 * 3600
	case "day":
		return ts / 86400 * 86400
	}
	t := time.Unix(ts, 0).UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}

// refusal says why an exact stream refuses an event, in the trigger's order, or "".
func (s *stream) refusal(ts int64, id any) string {
	p := s.pol
	if s.clockSet {
		if p.Late > 0 && ts < s.clock-p.Late {
			return "late"
		}
		if s.clock >= PeriodEnd(p.Period, ts)+p.Close {
			return "closed"
		}
	}
	if p.ID != nil {
		if _, seen := s.ids[id]; seen {
			return "repeat"
		}
	}
	return ""
}

func (s *stream) countRefused(reason string, key int32) error {
	k := refusedKey{reason, key}
	r := s.refused[k]
	if r == nil {
		var err error
		if r, err = s.loadRefused(reason, key); err != nil {
			return err
		}
		s.refused[k] = r
	}
	r.n++
	if !r.dirty {
		r.dirty = true
		s.refusedDirty = append(s.refusedDirty, r)
	}
	return nil
}

// put applies one event. vals are the stream's values in policy order and have been checked.
// It returns false when an exact stream refuses the event; the refusal is counted.
func (s *stream) put(ts int64, id any, key int32, vals []float64, line string) (bool, error) {
	if s.pol.Exact {
		if reason := s.refusal(ts, id); reason != "" {
			return false, s.countRefused(reason, key)
		}
	}
	// Derived values first: an event whose derived value is NULL changes nothing, as in SQL.
	nv := s.nv[:0]
	for i, v := range vals {
		if s.valInt[i] {
			nv = append(nv, num{isInt: true, i: int64(v)})
		} else {
			nv = append(nv, num{f: v})
		}
	}
	for i, d := range s.derived {
		r, ok := d(nv)
		if !ok {
			return false, &EventError{fmt.Sprintf("%s: derived value %s is null (a division by zero)", s.pol.Name, s.pol.Derived[i].Name)}
		}
		if !r.isInt && (math.IsInf(r.f, 0) || math.IsNaN(r.f)) {
			return false, &EventError{fmt.Sprintf("%s: derived value %s is not a finite number", s.pol.Name, s.pol.Derived[i].Name)}
		}
		nv = append(nv, r)
	}
	s.nv = nv
	nf := s.nf[:0]
	for _, n := range nv {
		nf = append(nf, n.float())
	}
	s.nf = nf

	if s.pol.Exact {
		if s.pol.ID != nil {
			s.ids[id] = ts
			s.newIDs = append(s.newIDs, id)
		}
		if !s.clockSet || ts > s.clock {
			s.clock, s.clockSet = ts, true
		}
		s.clockDirty = true
	}
	if s.pol.RawKeep != 0 || s.pol.RawUntilClosed {
		s.raw.ts = append(s.raw.ts, ts)
		s.raw.key = append(s.raw.key, key)
		s.raw.vals = append(s.raw.vals, vals...)
		if s.pol.ID != nil {
			s.raw.id = append(s.raw.id, id)
		}
		if s.line {
			s.raw.line = append(s.raw.line, line)
		}
	}

	// The anomaly check reads the baseline before this event updates it.
	var flag int64
	var t float64
	var b *baseRow
	a := s.anom
	guard := false
	if a != nil {
		x := nf[a.num]
		guard = !a.log || x > 0
		if guard {
			t = x
			if a.log {
				t = s.e.log(x)
			}
			var err error
			if b, err = s.getBase(key); err != nil {
				return false, err
			}
			if b != nil {
				j := t
				if a.change {
					j = t - b.prev
				}
				d := j - b.m
				if b.n >= a.warm && d*d > a.z2*b.v {
					flag = 1
					pw, err := s.window(a.per, ts/a.per.res*a.per.res, key)
					if err != nil {
						return false, err
					}
					if pw.an < a.keep {
						s.anoms.ts = append(s.anoms.ts, ts)
						s.anoms.key = append(s.anoms.key, key)
						s.anoms.vals = append(s.anoms.vals, vals...)
						s.anoms.z = append(s.anoms.z, d/math.Sqrt(b.v))
						if s.line {
							s.anoms.line = append(s.anoms.line, line)
						}
					}
				}
			}
		}
	}

	for _, r := range s.rollups {
		w, err := s.window(r, ts/r.res*r.res, key)
		if err != nil {
			return false, err
		}
		w.add(ts, nf, flag)
		for qi, ni := range r.quantNum {
			w.addCell(qi, s.bucket(nf[ni]))
		}
		if !w.dirty {
			w.dirty = true
			r.dirty = append(r.dirty, w)
		}
	}

	if r := s.sampleR; r != nil {
		w := r.cur[key]
		n, N := w.n, s.sampleN
		if n <= N || ((n*2654435761+97)%4294967296)%n < N {
			slot := n - 1
			if n > N {
				slot = ((n*1103515245 + 12345) % 2147483648) % N
			}
			if w.samp == nil {
				w.samp = make([]*sampleSlot, N)
			}
			sl := w.samp[slot]
			if sl == nil {
				sl = &sampleSlot{vals: make([]float64, s.nvals)}
				w.samp[slot] = sl
			}
			sl.ts = ts
			copy(sl.vals, vals)
			sl.line = line
			sl.dirty = true
		}
	}

	for _, g := range s.groups {
		if err := g.add(ts, key, nf); err != nil {
			return false, err
		}
	}

	if a != nil && guard {
		if a.change {
			if b == nil {
				b = &baseRow{prev: t}
				s.base[key] = b
			} else {
				d := t - b.prev
				dm := d - b.m
				if !(b.n >= a.warm && dm*dm > a.z2*b.v) {
					a.update(b, d)
				}
				b.prev = t
			}
			b.dirty = true
		} else if flag == 0 {
			if b == nil {
				b = &baseRow{n: 1, m: t}
				s.base[key] = b
			} else {
				a.update(b, t)
			}
			b.dirty = true
		}
	}
	return true, nil
}

// update moves a baseline towards x, exactly as the trigger's upsert does.
func (a *anomalyRule) update(b *baseRow, x float64) {
	if b.n < a.warm {
		n1 := float64(b.n + 1)
		m := b.m + (x-b.m)/n1
		v := (float64(b.n)*b.v + (x-b.m)*(x-(b.m+(x-b.m)/n1))) / n1
		b.m, b.v = m, v
	} else {
		sd := math.Sqrt(b.v)
		// min(max(x - m, -clip * sd), clip * sd), with SQLite's rules for ties.
		dc := x - b.m
		if lo := -a.clip * sd; dc < lo {
			dc = lo
		}
		if hi := a.clip * sd; !(dc < hi) {
			dc = hi
		}
		m := b.m + a.alpha*dc
		v := (1 - a.alpha) * (b.v + a.alpha*dc*dc/a.huber)
		b.m, b.v = m, v
	}
	b.n++
}

// bucket is the sketch bucket of x: ceil(ln(x) / ln(gamma)), or the zero bucket.
func (s *stream) bucket(x float64) int64 {
	if x > 0 {
		return int64(math.Ceil(s.e.log(x) / s.lay.LnGamma))
	}
	return compile.ZeroBucket
}

// window returns the summary of window w for a key, from memory, from the file, or new.
func (s *stream) window(r *rollup, w int64, key int32) (*window, error) {
	if c := r.cur[key]; c != nil && c.w == w {
		return c, nil
	}
	k := winKey{w, key}
	if win, ok := r.wins[k]; ok {
		r.cur[key] = win
		return win, nil
	}
	win := &window{w: w, key: key, st: make([]float64, 6*len(s.lay.Numbers))}
	if len(r.quantNum) > 0 {
		win.cells = make([]map[int64]*cell, len(r.quantNum))
		win.dirtyCells = make([][]*cell, len(r.quantNum))
		for i := range win.cells {
			win.cells[i] = map[int64]*cell{}
		}
	}
	if w <= r.water[key] {
		// The file may hold this window: carry on from what it has.
		if err := s.loadWindow(r, win); err != nil {
			return nil, err
		}
	} else {
		r.water[key] = w
	}
	r.wins[k] = win
	r.cur[key] = win
	return win, nil
}

func (w *window) add(ts int64, nf []float64, flag int64) {
	if w.n == 0 {
		w.n, w.firstTS, w.lastTS, w.an = 1, ts, ts, flag
		for i, x := range nf {
			st := w.st[6*i : 6*i+6]
			st[0], st[1], st[2], st[3], st[4], st[5] = x, x*x, x, x, x, x
		}
		return
	}
	w.n++
	for i, x := range nf {
		st := w.st[6*i : 6*i+6]
		st[0] = st[0] + x
		st[1] = st[1] + x*x
		if !(st[2] < x) {
			st[2] = x
		}
		if st[3] < x {
			st[3] = x
		}
		if ts < w.firstTS {
			st[4] = x
		}
		if ts >= w.lastTS {
			st[5] = x
		}
	}
	if ts < w.firstTS {
		w.firstTS = ts
	}
	if ts > w.lastTS {
		w.lastTS = ts
	}
	w.an += flag
}

func (w *window) addCell(qi int, b int64) {
	c := w.cells[qi][b]
	if c == nil {
		c = &cell{b: b}
		w.cells[qi][b] = c
	}
	c.n++
	if !c.dirty {
		c.dirty = true
		w.dirtyCells[qi] = append(w.dirtyCells[qi], c)
	}
}

// getBase returns the baseline of a key, asking the file once if it is not in memory.
func (s *stream) getBase(key int32) (*baseRow, error) {
	if b := s.base[key]; b != nil {
		return b, nil
	}
	if s.baseSeen[key] {
		return nil, nil
	}
	s.baseSeen[key] = true
	b, err := s.loadBase(key)
	if err != nil {
		return nil, err
	}
	s.base[key] = b
	return b, nil
}

// period returns the per period an event time falls in, as the text strftime makes, and its range.
func (g *group) period(ts int64) (string, int64, int64) {
	switch g.lay.Per {
	case "hour":
		st := floorDiv(ts, 3600) * 3600
		return time.Unix(st, 0).UTC().Format(g.goFmt), st, st + 3600
	case "day":
		st := floorDiv(ts, 86400) * 86400
		return time.Unix(st, 0).UTC().Format(g.goFmt), st, st + 86400
	case "month":
		t := time.Unix(ts, 0).UTC()
		st := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return st.Format(g.goFmt), st.Unix(), st.AddDate(0, 1, 0).Unix()
	}
	return "", math.MinInt64, math.MaxInt64
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// gkey returns the group key id for a stream key id.
func (g *group) gkey(key int32) int32 {
	if id := g.proj[key]; id >= 0 {
		return id
	}
	full := g.s.keys[key]
	tuple := make([]any, len(g.byIdx))
	for i, ki := range g.byIdx {
		tuple[i] = full[ki]
	}
	var b []byte
	for _, v := range tuple {
		switch x := v.(type) {
		case string:
			b = binary.AppendUvarint(append(b, 't'), uint64(len(x)))
			b = append(b, x...)
		case int64:
			b = binary.BigEndian.AppendUint64(append(b, 'i'), uint64(x))
		}
	}
	id, ok := g.gindex[string(b)]
	if !ok {
		id = int32(len(g.gkeys))
		g.gindex[string(b)] = id
		g.gkeys = append(g.gkeys, tuple)
		g.cur = append(g.cur, nil)
	}
	g.proj[key] = id
	return id
}

func (g *group) add(ts int64, key int32, nf []float64) error {
	gk := g.gkey(key)
	row := g.cur[gk]
	if row == nil || ts < row.pStart || ts >= row.pEnd {
		period, st, en := g.period(ts)
		k := groupKey{gk, period}
		row = g.rows[k]
		if row == nil {
			row = &groupRow{key: gk, period: period, pStart: st, pEnd: en}
			if g.lay.Quant {
				row.cells = map[int64]*cell{}
			} else {
				row.vals = make([]float64, len(g.sumN)+len(g.minN)+len(g.maxN)+len(g.fstN)+len(g.lstN))
			}
			if err := g.load(row); err != nil {
				return err
			}
			g.rows[k] = row
		}
		g.cur[gk] = row
	}
	if !row.dirty {
		row.dirty = true
		g.dirty = append(g.dirty, row)
	}
	if g.lay.Quant {
		b := g.s.bucket(nf[g.quantN])
		c := row.cells[b]
		if c == nil {
			c = &cell{b: b}
			row.cells[b] = c
		}
		c.n++
		if !c.dirty {
			c.dirty = true
			row.dirtyCells = append(row.dirtyCells, c)
		}
		return nil
	}
	v := row.vals
	if row.n == 0 {
		row.n, row.firstTS, row.lastTS = 1, ts, ts
		i := 0
		for _, lists := range [][]int{g.sumN, g.minN, g.maxN, g.fstN, g.lstN} {
			for _, ni := range lists {
				v[i] = nf[ni]
				i++
			}
		}
		return nil
	}
	row.n++
	i := 0
	for _, ni := range g.sumN {
		v[i] = v[i] + nf[ni]
		i++
	}
	for _, ni := range g.minN {
		if x := nf[ni]; !(v[i] < x) {
			v[i] = x
		}
		i++
	}
	for _, ni := range g.maxN {
		if x := nf[ni]; v[i] < x {
			v[i] = x
		}
		i++
	}
	for _, ni := range g.fstN {
		if ts < row.firstTS {
			v[i] = nf[ni]
		}
		i++
	}
	for _, ni := range g.lstN {
		if ts >= row.lastTS {
			v[i] = nf[ni]
		}
		i++
	}
	if ts < row.firstTS {
		row.firstTS = ts
	}
	if ts > row.lastTS {
		row.lastTS = ts
	}
	return nil
}
