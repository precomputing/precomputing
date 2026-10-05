package engine

import (
	"fmt"
	"strings"
)

// The statements a checkpoint runs. Rows the Engine holds in full are written whole with
// INSERT OR REPLACE; raw events and anomalies are appended.

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func (s *stream) keyNames() []string {
	var out []string
	for _, k := range s.pol.Keys {
		out = append(out, k.Name)
	}
	return out
}

func (s *stream) keyTypes() []byte {
	var t []byte
	for _, text := range s.keyText {
		if text {
			t = append(t, 't')
		} else {
			t = append(t, 'i')
		}
	}
	return t
}

func (s *stream) valTypes() []byte {
	var t []byte
	for _, i := range s.valInt {
		if i {
			t = append(t, 'i')
		} else {
			t = append(t, 'f')
		}
	}
	return t
}

func (s *stream) bindKey(b *Block, key int32) {
	for _, v := range s.keys[key] {
		switch x := v.(type) {
		case string:
			b.text(x)
		case int64:
			b.int(x)
		}
	}
}

func (s *stream) bindVals(b *Block, vals []float64) {
	for i, v := range vals {
		if s.valInt[i] {
			b.int(int64(v))
		} else {
			b.real(v)
		}
	}
}

func insertSQL(verb, table string, cols []string) string {
	return fmt.Sprintf("%s INTO %s (%s) VALUES (%s)", verb, table, strings.Join(cols, ", "), placeholders(len(cols)))
}

// blocks appends this stream's part of a checkpoint.
func (s *stream) blocks(bt *Batch, now int64) {
	keys := s.keyNames()
	kt := s.keyTypes()
	vals := valueNames(s)
	vt := s.valTypes()

	if n := len(s.raw.ts); n > 0 {
		idCols, idTypes := []string{}, []byte{}
		if f := s.pol.ID; f != nil {
			idCols = append(idCols, f.Name)
			if f.Type == "integer" {
				idTypes = append(idTypes, 'i')
			} else {
				idTypes = append(idTypes, 't')
			}
		}
		cols := append(append(append([]string{"ts"}, idCols...), keys...), vals...)
		types := append(append(append([]byte{'i'}, idTypes...), kt...), vt...)
		if s.line {
			cols, types = append(cols, "line"), append(types, 't')
		}
		b := newBlock(insertSQL("INSERT", s.lay.Raw, cols), types)
		cutoff, skip := now-s.pol.RawKeep, s.pol.RawKeep > 0
		if s.pol.RawUntilClosed {
			cutoff, skip = PeriodStart(s.pol.Period, now-s.pol.Close-s.pol.RawAfterClose), true
		}
		for i := 0; i < n; i++ {
			ts := s.raw.ts[i]
			// Distill would delete it straight away, so it is not written at all.
			if skip && ts < cutoff {
				continue
			}
			b.int(ts)
			if s.pol.ID != nil {
				bindAny(b, s.raw.id[i])
			}
			s.bindKey(b, s.raw.key[i])
			s.bindVals(b, s.raw.vals[i*s.nvals:(i+1)*s.nvals])
			if s.line {
				b.text(s.raw.line[i])
			}
			b.row()
		}
		if b.N > 0 {
			bt.Blocks = append(bt.Blocks, b)
		}
	}

	var wb *Block
	sk := map[string]*Block{}
	var sb *Block
	for _, r := range s.rollups {
		for _, w := range r.dirty {
			if wb == nil {
				cols := append(append([]string{"res", "w"}, keys...), "n", "first_ts", "last_ts", "an")
				types := append(append([]byte{'i', 'i'}, kt...), 'i', 'i', 'i', 'i')
				for _, f := range s.lay.Numbers {
					cols = append(cols, f+"_sum", f+"_sumsq", f+"_min", f+"_max", f+"_first", f+"_last")
					types = append(types, 'f', 'f', 'f', 'f', 'f', 'f')
				}
				wb = newBlock(insertSQL("INSERT OR REPLACE", s.lay.Win, cols), types)
			}
			wb.int(r.res)
			wb.int(w.w)
			s.bindKey(wb, w.key)
			wb.int(w.n)
			wb.int(w.firstTS)
			wb.int(w.lastTS)
			wb.int(w.an)
			for _, x := range w.st {
				wb.real(x)
			}
			wb.row()
			for qi, table := range r.quantSk {
				if len(w.dirtyCells[qi]) == 0 {
					continue
				}
				b := sk[table]
				if b == nil {
					cols := append(append([]string{"res", "w"}, keys...), "b", "n")
					b = newBlock(insertSQL("INSERT OR REPLACE", table, cols), append(append([]byte{'i', 'i'}, kt...), 'i', 'i'))
					sk[table] = b
				}
				for _, c := range w.dirtyCells[qi] {
					b.int(r.res)
					b.int(w.w)
					s.bindKey(b, w.key)
					b.int(c.b)
					b.int(c.n)
					b.row()
				}
			}
			if r.samples && w.samp != nil {
				for slot, sl := range w.samp {
					if sl == nil || !sl.dirty {
						continue
					}
					if sb == nil {
						cols := append(append(append([]string{"res", "w"}, keys...), "slot", "ts"), vals...)
						types := append(append(append([]byte{'i', 'i'}, kt...), 'i', 'i'), vt...)
						if s.line {
							cols, types = append(cols, "line"), append(types, 't')
						}
						sb = newBlock(insertSQL("INSERT OR REPLACE", s.lay.Sample, cols), types)
					}
					sb.int(r.res)
					sb.int(w.w)
					s.bindKey(sb, w.key)
					sb.int(int64(slot))
					sb.int(sl.ts)
					s.bindVals(sb, sl.vals)
					if s.line {
						sb.text(sl.line)
					}
					sb.row()
				}
			}
		}
	}
	if wb != nil {
		bt.Blocks = append(bt.Blocks, wb)
	}
	for _, sl := range s.lay.Sketches {
		if b := sk[sl.Table]; b != nil {
			bt.Blocks = append(bt.Blocks, b)
		}
	}
	if sb != nil {
		bt.Blocks = append(bt.Blocks, sb)
	}

	if a := s.anom; a != nil {
		var bb *Block
		for key, b := range s.base {
			if b == nil || !b.dirty {
				continue
			}
			if bb == nil {
				cols := append([]string{}, keys...)
				types := append([]byte{}, kt...)
				if len(keys) == 0 {
					cols, types = []string{"g"}, []byte{'i'}
				}
				cols = append(cols, "n", "m", "var")
				types = append(types, 'i', 'f', 'f')
				if a.change {
					cols = append(cols, "prev")
					types = append(types, 'f')
				}
				bb = newBlock(insertSQL("INSERT OR REPLACE", s.lay.Base, cols), types)
			}
			if len(keys) == 0 {
				bb.int(1)
			} else {
				s.bindKey(bb, int32(key))
			}
			bb.int(b.n)
			bb.real(b.m)
			bb.real(b.v)
			if a.change {
				bb.real(b.prev)
			}
			bb.row()
		}
		if bb != nil {
			bt.Blocks = append(bt.Blocks, bb)
		}
		if n := len(s.anoms.ts); n > 0 {
			cols := append(append([]string{"ts"}, keys...), vals...)
			types := append(append([]byte{'i'}, kt...), vt...)
			if s.line {
				cols, types = append(cols, "line"), append(types, 't')
			}
			cols, types = append(cols, "z"), append(types, 'f')
			b := newBlock(insertSQL("INSERT", s.lay.Anomaly, cols), types)
			for i := 0; i < n; i++ {
				b.int(s.anoms.ts[i])
				s.bindKey(b, s.anoms.key[i])
				s.bindVals(b, s.anoms.vals[i*s.nvals:(i+1)*s.nvals])
				if s.line {
					b.text(s.anoms.line[i])
				}
				b.real(s.anoms.z[i])
				b.row()
			}
			bt.Blocks = append(bt.Blocks, b)
		}
	}

	if s.pol.Exact {
		if f := s.pol.ID; f != nil && len(s.newIDs) > 0 {
			t := byte('t')
			if f.Type == "integer" {
				t = 'i'
			}
			b := newBlock(insertSQL("INSERT OR REPLACE", s.lay.IDs, []string{f.Name, "ts"}), []byte{t, 'i'})
			for _, id := range s.newIDs {
				ts, ok := s.ids[id]
				if !ok {
					continue
				}
				bindAny(b, id)
				b.int(ts)
				b.row()
			}
			bt.Blocks = append(bt.Blocks, b)
		}
		if s.clockDirty {
			b := newBlock(insertSQL("INSERT OR REPLACE", s.lay.Clock, []string{"g", "newest"}), []byte{'i', 'i'})
			b.int(1)
			b.int(s.clock)
			b.row()
			bt.Blocks = append(bt.Blocks, b)
		}
		if len(s.refusedDirty) > 0 {
			cols := append(append([]string{"reason"}, keys...), "n")
			b := newBlock(insertSQL("INSERT OR REPLACE", s.lay.Refused, cols), append(append([]byte{'t'}, kt...), 'i'))
			for _, r := range s.refusedDirty {
				b.text(r.reason)
				s.bindKey(b, r.key)
				b.int(r.n)
				b.row()
			}
			bt.Blocks = append(bt.Blocks, b)
		}
	}

	for _, g := range s.groups {
		if len(g.dirty) == 0 {
			continue
		}
		var cols []string
		var types []byte
		if g.lay.NoKey {
			cols, types = []string{"g"}, []byte{'i'}
		}
		for _, bname := range g.lay.By {
			cols = append(cols, bname)
			for i, k := range s.pol.Keys {
				if k.Name == bname {
					types = append(types, kt[i])
				}
			}
		}
		if g.lay.Per != "" {
			cols = append(cols, "period")
			types = append(types, 't')
		}
		if g.lay.Quant {
			cols = append(cols, "b", "n")
			types = append(types, 'i', 'i')
		} else {
			gc := g.columns()
			cols = append(cols, gc...)
			types = append(types, 'i')
			for range gc[1:] {
				types = append(types, 'f')
			}
			if g.lay.FirstTS {
				types[len(types)-1-boolInt(g.lay.LastTS)] = 'i'
			}
			if g.lay.LastTS {
				types[len(types)-1] = 'i'
			}
		}
		b := newBlock(insertSQL("INSERT OR REPLACE", g.lay.Table, cols), types)
		for _, row := range g.dirty {
			bindGroupKey := func() {
				if g.lay.NoKey {
					b.int(1)
				}
				for _, v := range g.gkeys[row.key] {
					switch x := v.(type) {
					case string:
						b.text(x)
					case int64:
						b.int(x)
					}
				}
				if g.lay.Per != "" {
					b.text(row.period)
				}
			}
			if g.lay.Quant {
				for _, c := range row.dirtyCells {
					bindGroupKey()
					b.int(c.b)
					b.int(c.n)
					b.row()
				}
				continue
			}
			bindGroupKey()
			b.int(row.n)
			for _, x := range row.vals {
				b.real(x)
			}
			if g.lay.FirstTS {
				b.int(row.firstTS)
			}
			if g.lay.LastTS {
				b.int(row.lastTS)
			}
			b.row()
		}
		bt.Blocks = append(bt.Blocks, b)
	}
}

func bindAny(b *Block, v any) {
	switch x := v.(type) {
	case string:
		b.text(x)
	case int64:
		b.int(x)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// written marks everything in the checkpoint as clean and lets closed windows go from memory.
func (s *stream) written(now int64) {
	s.raw.ts, s.raw.id, s.raw.key, s.raw.vals, s.raw.line = s.raw.ts[:0], s.raw.id[:0], s.raw.key[:0], s.raw.vals[:0], s.raw.line[:0]
	if s.pol.Exact {
		s.newIDs = s.newIDs[:0]
		s.clockDirty = false
		for _, r := range s.refusedDirty {
			r.dirty = false
		}
		s.refusedDirty = s.refusedDirty[:0]
		// Distill has forgotten identifiers older than the repeat window: so does memory.
		if s.pol.ID != nil {
			for id, ts := range s.ids {
				if ts < now-s.pol.Repeats {
					delete(s.ids, id)
				}
			}
		}
	}
	s.anoms.ts, s.anoms.key, s.anoms.vals, s.anoms.z, s.anoms.line = s.anoms.ts[:0], s.anoms.key[:0], s.anoms.vals[:0], s.anoms.z[:0], s.anoms.line[:0]
	for _, r := range s.rollups {
		for _, w := range r.dirty {
			w.dirty = false
			for qi := range w.dirtyCells {
				for _, c := range w.dirtyCells[qi] {
					c.dirty = false
				}
				w.dirtyCells[qi] = w.dirtyCells[qi][:0]
			}
			for _, sl := range w.samp {
				if sl != nil {
					sl.dirty = false
				}
			}
		}
		r.dirty = r.dirty[:0]
		for k, w := range r.wins {
			if w.w+r.res <= now {
				delete(r.wins, k)
				if r.cur[w.key] == w {
					r.cur[w.key] = nil
				}
			}
		}
	}
	for _, b := range s.base {
		if b != nil {
			b.dirty = false
		}
	}
	for _, g := range s.groups {
		for _, row := range g.dirty {
			row.dirty = false
			for _, c := range row.dirtyCells {
				c.dirty = false
			}
			row.dirtyCells = row.dirtyCells[:0]
		}
		g.dirty = g.dirty[:0]
		if g.lay.Per != "" {
			for k, row := range g.rows {
				if row.pEnd <= now {
					delete(g.rows, k)
					if g.cur[row.key] == row {
						g.cur[row.key] = nil
					}
				}
			}
		}
	}
}

// resident counts the windows held in memory.
func (s *stream) resident() int {
	n := 0
	for _, r := range s.rollups {
		n += len(r.wins)
	}
	return n
}
