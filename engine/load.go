package engine

import (
	"fmt"
	"strings"
)

// The Engine reads its file in three cases: when it opens (sources and the newest window of every
// key), when an event touches a window older than the newest one it knows (a late event, or the
// window it was filling when it stopped), and the first time it meets a key's baseline or a
// precompute row.

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

// keyWhere is "k1 = ? AND k2 = ?" for a stream's keys, with a leading " AND " when not empty.
func (s *stream) keyWhere() string {
	var b strings.Builder
	for _, k := range s.pol.Keys {
		b.WriteString(" AND ")
		b.WriteString(k.Name)
		b.WriteString(" = ?")
	}
	return b.String()
}

func (s *stream) keyArgs(key int32, args []any) []any {
	return append(args, s.keys[key]...)
}

func (s *stream) loadWindow(r *rollup, win *window) error {
	e := s.e
	e.stats.Reads++
	cols := []string{"n", "first_ts", "last_ts", "an"}
	for _, f := range s.lay.Numbers {
		cols = append(cols, f+"_sum", f+"_sumsq", f+"_min", f+"_max", f+"_first", f+"_last")
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE res = ? AND w = ?%s",
		strings.Join(cols, ", "), s.lay.Win, s.keyWhere())
	args := s.keyArgs(win.key, []any{r.res, win.w})
	err := e.store.Query(q, args, func(row []any) error {
		win.n, win.firstTS, win.lastTS, win.an = asInt(row[0]), asInt(row[1]), asInt(row[2]), asInt(row[3])
		for i := range win.st {
			win.st[i] = asFloat(row[4+i])
		}
		return nil
	})
	if err != nil {
		return err
	}
	for qi, table := range r.quantSk {
		q := fmt.Sprintf("SELECT b, n FROM %s WHERE res = ? AND w = ?%s", table, s.keyWhere())
		err := e.store.Query(q, args, func(row []any) error {
			b := asInt(row[0])
			win.cells[qi][b] = &cell{b: b, n: asInt(row[1])}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if r.samples {
		cols := append([]string{"slot", "ts"}, valueNames(s)...)
		if s.line {
			cols = append(cols, "line")
		}
		q := fmt.Sprintf("SELECT %s FROM %s WHERE res = ? AND w = ?%s",
			strings.Join(cols, ", "), s.lay.Sample, s.keyWhere())
		err := e.store.Query(q, args, func(row []any) error {
			slot := asInt(row[0])
			if slot < 0 || slot >= s.sampleN {
				return nil
			}
			if win.samp == nil {
				win.samp = make([]*sampleSlot, s.sampleN)
			}
			sl := &sampleSlot{ts: asInt(row[1]), vals: make([]float64, s.nvals)}
			for i := range sl.vals {
				sl.vals[i] = asFloat(row[2+i])
			}
			if s.line {
				sl.line, _ = row[2+s.nvals].(string)
			}
			win.samp[slot] = sl
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func valueNames(s *stream) []string {
	var out []string
	for _, v := range s.pol.Values {
		out = append(out, v.Name)
	}
	return out
}

func (s *stream) loadBase(key int32) (*baseRow, error) {
	s.e.stats.Reads++
	cols := "n, m, var"
	if s.anom.change {
		cols += ", prev"
	}
	where := "g = 1"
	var args []any
	if len(s.pol.Keys) > 0 {
		where = strings.TrimPrefix(s.keyWhere(), " AND ")
		args = s.keyArgs(key, nil)
	}
	var b *baseRow
	err := s.e.store.Query(fmt.Sprintf("SELECT %s FROM %s WHERE %s", cols, s.lay.Base, where), args, func(row []any) error {
		b = &baseRow{n: asInt(row[0]), m: asFloat(row[1]), v: asFloat(row[2])}
		if s.anom.change {
			b.prev = asFloat(row[3])
		}
		return nil
	})
	return b, err
}

func (g *group) where(row *groupRow) (string, []any) {
	var parts []string
	var args []any
	for i, b := range g.lay.By {
		parts = append(parts, b+" = ?")
		args = append(args, g.gkeys[row.key][i])
	}
	if g.lay.Per != "" {
		parts = append(parts, "period = ?")
		args = append(args, row.period)
	}
	if len(parts) == 0 {
		if g.lay.NoKey {
			return "g = 1", nil
		}
		return "1", nil
	}
	return strings.Join(parts, " AND "), args
}

func (g *group) load(row *groupRow) error {
	g.s.e.stats.Reads++
	where, args := g.where(row)
	if g.lay.Quant {
		return g.s.e.store.Query(fmt.Sprintf("SELECT b, n FROM %s WHERE %s", g.lay.Table, where), args, func(r []any) error {
			b := asInt(r[0])
			row.cells[b] = &cell{b: b, n: asInt(r[1])}
			return nil
		})
	}
	cols := g.columns()
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s", strings.Join(cols, ", "), g.lay.Table, where)
	return g.s.e.store.Query(q, args, func(r []any) error {
		row.n = asInt(r[0])
		for i := range row.vals {
			row.vals[i] = asFloat(r[1+i])
		}
		i := 1 + len(row.vals)
		if g.lay.FirstTS {
			row.firstTS = asInt(r[i])
			i++
		}
		if g.lay.LastTS {
			row.lastTS = asInt(r[i])
		}
		return nil
	})
}

// columns lists n, the value columns in the order of groupRow.vals, then first_ts and last_ts if kept.
func (g *group) columns() []string {
	cols := []string{"n"}
	for _, f := range g.lay.Sum {
		cols = append(cols, f+"_sum")
	}
	for _, f := range g.lay.Min {
		cols = append(cols, f+"_min")
	}
	for _, f := range g.lay.Max {
		cols = append(cols, f+"_max")
	}
	for _, f := range g.lay.First {
		cols = append(cols, f+"_first")
	}
	for _, f := range g.lay.Last {
		cols = append(cols, f+"_last")
	}
	if g.lay.FirstTS {
		cols = append(cols, "first_ts")
	}
	if g.lay.LastTS {
		cols = append(cols, "last_ts")
	}
	return cols
}

// loadWatermarks learns the newest window of every key in the file, so that new windows can be
// started without asking the file, and older ones are read from it.
func (s *stream) loadWatermarks() error {
	if s.lay.Win == "" {
		return nil
	}
	var keys []string
	for _, k := range s.pol.Keys {
		keys = append(keys, k.Name)
	}
	sel := strings.Join(append(append([]string{"res"}, keys...), "max(w)"), ", ")
	group := strings.Join(append([]string{"res"}, keys...), ", ")
	q := fmt.Sprintf("SELECT %s FROM %s GROUP BY %s", sel, s.lay.Win, group)
	return s.e.store.Query(q, nil, func(row []any) error {
		res := asInt(row[0])
		tuple := row[1 : 1+len(keys)]
		id, err := s.intern(tuple)
		if err != nil {
			return err
		}
		mw := asInt(row[1+len(keys)])
		for _, r := range s.rollups {
			if r.res == res && mw > r.water[id] {
				r.water[id] = mw
			}
		}
		return nil
	})
}

// loadExact reads an exact stream's identifiers and clock. Distill keeps only the identifiers of
// the repeat window, so they fit in memory.
func (s *stream) loadExact() error {
	if !s.pol.Exact {
		return nil
	}
	if s.pol.ID != nil {
		err := s.e.store.Query(fmt.Sprintf("SELECT %s, ts FROM %s", s.pol.ID.Name, s.lay.IDs), nil, func(row []any) error {
			id := row[0]
			if s.pol.ID.Type == "integer" {
				id = asInt(row[0])
			}
			s.ids[id] = asInt(row[1])
			return nil
		})
		if err != nil {
			return err
		}
	}
	return s.e.store.Query(fmt.Sprintf("SELECT newest FROM %s WHERE g = 1", s.lay.Clock), nil, func(row []any) error {
		s.clock, s.clockSet = asInt(row[0]), true
		return nil
	})
}

func (s *stream) loadRefused(reason string, key int32) (*refusedRow, error) {
	s.e.stats.Reads++
	r := &refusedRow{reason: reason, key: key}
	args := s.keyArgs(key, []any{reason})
	err := s.e.store.Query(fmt.Sprintf("SELECT n FROM %s WHERE reason = ?%s", s.lay.Refused, s.keyWhere()), args, func(row []any) error {
		r.n = asInt(row[0])
		return nil
	})
	return r, err
}
