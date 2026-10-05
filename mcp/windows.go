package mcp

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxWindowRows caps the windows one call reads before it merges them.
const maxWindowRows = 200000

// zeroBucket is the sketch bucket of zero and negative values (compile.ZeroBucket).
const zeroBucket = -1000000

var pctRE = regexp.MustCompile(`^p([0-9]{1,4})$`)

// quantileOf reads p50 as 0.5, p99 as 0.99 and p999 as 0.999.
func quantileOf(stat string) (float64, bool) {
	m := pctRE.FindStringSubmatch(stat)
	if m == nil {
		return 0, false
	}
	d, _ := strconv.Atoi(m[1])
	if d == 0 {
		return 0, false
	}
	return float64(d) / math.Pow(10, float64(len(m[1]))), true
}

// bucketValue is the value a sketch bucket stands for, the rule the compiled views use.
func bucketValue(b int64, gamma float64) float64 {
	if b == zeroBucket {
		return 0
	}
	return 2 * math.Pow(gamma, float64(b)) / (gamma + 1)
}

// streamNewest is the time of a stream's newest event.
func streamNewest(r Reader, s *stream) (int64, error) {
	var t int64
	for _, ro := range s.rollups {
		row, err := r.Read(fmt.Sprintf("SELECT max(last_ts) FROM %s WHERE res = ? AND w = (SELECT max(w) FROM %s WHERE res = ?)", quoteName(s.win), quoteName(s.win)), []any{ro.res, ro.res}, 0)
		if err != nil {
			return 0, err
		}
		if v := intAt(first(row), 0); v > t {
			t = v
		}
	}
	return t, nil
}

type agg struct {
	start           int64
	keys            []any
	n, an           int64
	firstTS, lastTS int64
	sum, sumsq      []float64
	min, max        []float64
	first, last     []float64
	buckets         map[string][][2]int64 // number -> (bucket, count), ascending
}

func getWindows(r Reader, a args) (string, error) {
	name, err := a.str("stream", true)
	if err != nil {
		return "", err
	}
	limit, err := a.integer("limit", 100, 1, 1000)
	if err != nil {
		return "", err
	}
	c, err := loadCatalog(r)
	if err != nil {
		return "", err
	}
	s, err := c.stream(name)
	if err != nil {
		return "", fmt.Errorf("get_windows: %v", err)
	}
	if len(s.rollups) == 0 {
		return "", fmt.Errorf("get_windows: stream %s keeps no windows; get_kept reads its events", s.name)
	}
	now, err := streamNewest(r, s)
	if err != nil {
		return "", err
	}
	to, hasTo, err := a.when("to")
	if err != nil {
		return "", err
	}
	if !hasTo {
		to = now + 1
	}
	from, hasFrom, err := a.when("from")
	if err != nil {
		return "", err
	}
	if !hasFrom {
		from = to - 3600
	}
	if from >= to {
		return "", fmt.Errorf("get_windows: from (%s) must come before to (%s)", when(from), when(to))
	}
	fs, err := a.where(s.keys, s.intKey)
	if err != nil {
		return "", err
	}
	by, hasBy, err := a.list("by")
	if err != nil {
		return "", err
	}
	if !hasBy {
		by = s.keys
	}
	for _, k := range by {
		ok := false
		for _, sk := range s.keys {
			ok = ok || sk == k
		}
		if !ok {
			return "", fmt.Errorf("get_windows: by names %s, which is not a key of %s; its keys are %s", k, s.name, strings.Join(s.keys, ", "))
		}
	}

	// Which figures.
	statList, explicit, err := a.list("stats")
	if err != nil {
		return "", err
	}
	var pcts []string
	var plain []string
	if explicit {
		for _, st := range statList {
			st = strings.ToLower(st)
			switch st {
			case "n", "count", "avg", "min", "max", "first", "last", "sum", "std":
				if st == "count" {
					st = "n"
				}
				plain = append(plain, st)
			default:
				if _, ok := quantileOf(st); ok {
					pcts = append(pcts, st)
				} else {
					return "", fmt.Errorf("get_windows: %q is not a figure; use n, avg, min, max, first, last, sum, std or a percentile such as p99", st)
				}
			}
		}
	} else {
		plain = []string{"n", "avg", "min", "max", "first", "last", "sum"}
	}

	// Which windows to read.
	everyText, err := a.str("every", false)
	if err != nil {
		return "", err
	}
	all := strings.EqualFold(everyText, "all")
	var every int64
	if everyText != "" && !all {
		if every, err = parseDuration(everyText); err != nil {
			return "", fmt.Errorf("get_windows: every: %v", err)
		}
	}
	type present struct {
		n, minW, maxW int64
	}
	have := map[int64]present{}
	for _, ro := range s.rollups {
		row, err := r.Read(fmt.Sprintf("SELECT count(*), min(w), max(w) FROM %s WHERE res = ?", quoteName(s.win)), []any{ro.res}, 0)
		if err != nil {
			return "", err
		}
		x := first(row)
		have[ro.res] = present{intAt(x, 0), intAt(x, 1), intAt(x, 2)}
	}
	start := have[s.rollups[len(s.rollups)-1].res].minW // the oldest data, from the longest-kept windows
	covers := func(ro rollup) bool {
		p := have[ro.res]
		lo := from
		if start > lo {
			lo = start
		}
		return p.n > 0 && p.minW <= lo
	}
	sketched := func(ro rollup) bool { return len(ro.quantiles) > 0 }
	aligned := func(ro rollup) bool { return from%ro.res == 0 && to%ro.res == 0 }
	var cands []rollup
	for _, ro := range s.rollups {
		if every > 0 && every%ro.res != 0 {
			continue
		}
		if covers(ro) {
			cands = append(cands, ro)
		}
	}
	if len(cands) == 0 {
		var kept []string
		for _, ro := range s.rollups {
			p := have[ro.res]
			if p.n == 0 {
				kept = append(kept, fmt.Sprintf("%s (none left)", durationText(ro.res)))
			} else {
				kept = append(kept, fmt.Sprintf("%s from %s to %s", durationText(ro.res), when(p.minW), when(p.maxW+ro.res)))
			}
		}
		if every > 0 {
			return "", fmt.Errorf("get_windows: no kept windows fit every %s for this range; %s keeps %s", durationText(every), s.name, strings.Join(kept, "; "))
		}
		return "", fmt.Errorf("get_windows: nothing is kept for this range; %s keeps %s", s.name, strings.Join(kept, "; "))
	}
	var ro rollup
	if every == 0 && !all {
		// Automatic: the finest windows that give at most 60 rows, else the coarsest. Asked for
		// percentiles, windows with a sketch come first.
		pick := func(need func(rollup) bool) (rollup, bool) {
			for _, x := range cands {
				if need(x) && (to-from+x.res-1)/x.res <= 60 {
					return x, true
				}
			}
			for i := len(cands) - 1; i >= 0; i-- {
				if need(cands[i]) {
					return cands[i], true
				}
			}
			return rollup{}, false
		}
		anyRollup := func(rollup) bool { return true }
		ok := false
		if len(pcts) > 0 {
			ro, ok = pick(sketched)
		}
		if !ok {
			ro, _ = pick(anyRollup)
		}
		every = ro.res
	} else {
		wantSketch := len(pcts) > 0 || !explicit
		// Windows that fit the range exactly come first, then windows with a sketch when
		// percentiles are wanted; among windows that fit, the coarsest (fewest to read), and
		// among the rest the finest (least left out at the edges).
		sort.SliceStable(cands, func(i, j int) bool {
			ai, aj := aligned(cands[i]), aligned(cands[j])
			if ai != aj {
				return ai
			}
			if wantSketch {
				si, sj := sketched(cands[i]), sketched(cands[j])
				if si != sj {
					return si
				}
			}
			if ai {
				return cands[i].res > cands[j].res
			}
			return cands[i].res < cands[j].res
		})
		ro = cands[0]
	}
	var nums []string // numbers with percentiles at this resolution
	if len(ro.quantiles) > 0 {
		for _, n := range s.numbers {
			if sk := s.sketches[n]; sk != nil && sk.has(ro.res) {
				nums = append(nums, n)
			}
		}
	}
	var notes []string
	if !explicit && len(nums) > 0 {
		pcts = []string{"p50", "p95", "p99"}
	}
	if len(pcts) > 0 && len(nums) == 0 {
		notes = append(notes, fmt.Sprintf("No percentiles: the %s windows keep no sketch.", durationText(ro.res)))
		pcts = nil
	}

	// Read the windows and merge them into rows.
	cols := []string{"w"}
	for _, k := range by {
		cols = append(cols, quoteName(k))
	}
	cols = append(cols, "n", "first_ts", "last_ts", "an")
	for _, n := range s.numbers {
		for _, suf := range []string{"_sum", "_sumsq", "_min", "_max", "_first", "_last"} {
			cols = append(cols, quoteName(n+suf))
		}
	}
	cond, fargs := sqlFilters(fs, "")
	sql := fmt.Sprintf("SELECT %s FROM %s WHERE res = ? AND w >= ? AND w <= ?", strings.Join(cols, ", "), quoteName(s.win))
	qargs := []any{ro.res, from, to - ro.res}
	if cond != "" {
		sql += " AND " + cond
		qargs = append(qargs, fargs...)
	}
	order := []string{"w"}
	for _, k := range by {
		order = append(order, quoteName(k))
	}
	sql += " ORDER BY " + strings.Join(order, ", ")
	rows, err := r.Read(sql, qargs, maxWindowRows)
	if err != nil {
		return "", fmt.Errorf("get_windows: %v", err)
	}
	if rows.More {
		return "", fmt.Errorf("get_windows: more than %d windows; narrow the range or the keys, or use a coarser every", maxWindowRows)
	}
	bucket := func(w int64) int64 {
		if all {
			return 0
		}
		return w - ((w%every)+every)%every
	}
	keyOf := func(start int64, ks []any) string {
		var sb strings.Builder
		sb.WriteString(strconv.FormatInt(start, 10))
		for _, k := range ks {
			sb.WriteByte(0)
			sb.WriteString(cell(k))
		}
		return sb.String()
	}
	nk := len(by)
	nn := len(s.numbers)
	groups := map[string]*agg{}
	var list []*agg
	var minW, maxW int64 = math.MaxInt64, math.MinInt64
	for _, row := range rows.Rows {
		w, _ := asInt(row[0])
		if w < minW {
			minW = w
		}
		if w > maxW {
			maxW = w
		}
		ks := row[1 : 1+nk]
		k := keyOf(bucket(w), ks)
		g := groups[k]
		base := 1 + nk
		n, _ := asInt(row[base])
		fts, _ := asInt(row[base+1])
		lts, _ := asInt(row[base+2])
		an, _ := asInt(row[base+3])
		if g == nil {
			g = &agg{start: bucket(w), keys: ks, firstTS: fts, lastTS: lts,
				sum: make([]float64, nn), sumsq: make([]float64, nn), min: make([]float64, nn), max: make([]float64, nn),
				first: make([]float64, nn), last: make([]float64, nn)}
			for i := range g.min {
				g.min[i], g.max[i] = math.Inf(1), math.Inf(-1)
			}
			groups[k] = g
			list = append(list, g)
		}
		g.n += n
		g.an += an
		for i := 0; i < nn; i++ {
			v := func(j int) float64 { f, _ := asFloat(row[base+4+6*i+j]); return f }
			g.sum[i] += v(0)
			g.sumsq[i] += v(1)
			g.min[i] = math.Min(g.min[i], v(2))
			g.max[i] = math.Max(g.max[i], v(3))
		}
		if fts < g.firstTS || n == g.n {
			g.firstTS = fts
			for i := 0; i < nn; i++ {
				g.first[i], _ = asFloat(row[base+4+6*i+4])
			}
		}
		if lts > g.lastTS || n == g.n {
			g.lastTS = lts
			for i := 0; i < nn; i++ {
				g.last[i], _ = asFloat(row[base+4+6*i+5])
			}
		}
	}

	// Percentiles: add up the sketch buckets of the windows in each row.
	if len(pcts) > 0 {
		for _, n := range nums {
			sk := s.sketches[n]
			kcols := []string{}
			for _, k := range by {
				kcols = append(kcols, quoteName(k))
			}
			startExpr := "0"
			if !all {
				startExpr = fmt.Sprintf("w - (w %% %d)", every)
			}
			gb := append([]string{"1"}, kcols...)
			sel := append(append([]string{startExpr}, kcols...), "b", "sum(n)")
			q := fmt.Sprintf("SELECT %s FROM %s WHERE res = ? AND w >= ? AND w <= ?", strings.Join(sel, ", "), quoteName(sk.table))
			qa := []any{ro.res, from, to - ro.res}
			if cond != "" {
				q += " AND " + cond
				qa = append(qa, fargs...)
			}
			q += " GROUP BY " + strings.Join(append(gb, "b"), ", ") + " ORDER BY " + strings.Join(append(gb, "b"), ", ")
			brows, err := r.Read(q, qa, 0)
			if err != nil {
				return "", fmt.Errorf("get_windows: %v", err)
			}
			for _, row := range brows.Rows {
				st, _ := asInt(row[0])
				g := groups[keyOf(st, row[1:1+nk])]
				if g == nil {
					continue
				}
				if g.buckets == nil {
					g.buckets = map[string][][2]int64{}
				}
				b, _ := asInt(row[1+nk])
				cnt, _ := asInt(row[2+nk])
				g.buckets[n] = append(g.buckets[n], [2]int64{b, cnt})
			}
		}
	}

	if all { // one summary per key: in key order
		sort.SliceStable(list, func(i, j int) bool {
			for k := range list[i].keys {
				a, b := list[i].keys[k], list[j].keys[k]
				if x, ok := asFloat(a); ok {
					if y, ok := asFloat(b); ok && x != y {
						return x < y
					}
				}
				if cell(a) != cell(b) {
					return cell(a) < cell(b)
				}
			}
			return false
		})
	}

	// Write the rows.
	pinned := fixed(fs)
	var head []string
	if !all {
		head = append(head, "start")
	}
	var keyIdx []int
	for i, k := range by {
		if !pinned[k] {
			head = append(head, k)
			keyIdx = append(keyIdx, i)
		}
	}
	hasN := false
	for _, st := range plain {
		if st == "n" {
			hasN = true
		}
	}
	if hasN || len(s.numbers) == 0 {
		head = append(head, "n")
	}
	for _, n := range s.numbers {
		for _, st := range plain {
			if st != "n" {
				head = append(head, n+"_"+st)
			}
		}
		for _, p := range pcts {
			if s.sketches[n] != nil && s.sketches[n].has(ro.res) {
				head = append(head, n+"_"+p)
			}
		}
	}
	if s.anomalies != "" {
		head = append(head, "unusual")
	}
	t := &table{}
	t.line(head...)
	shown := 0
	for _, g := range list {
		if shown == limit {
			break
		}
		var f []string
		if !all {
			f = append(f, when(g.start))
		}
		for _, i := range keyIdx {
			f = append(f, cell(g.keys[i]))
		}
		if hasN || len(s.numbers) == 0 {
			f = append(f, strconv.FormatInt(g.n, 10))
		}
		for i, n := range s.numbers {
			for _, st := range plain {
				switch st {
				case "avg":
					f = append(f, num(g.sum[i]/float64(g.n)))
				case "min":
					f = append(f, num(g.min[i]))
				case "max":
					f = append(f, num(g.max[i]))
				case "first":
					f = append(f, num(g.first[i]))
				case "last":
					f = append(f, num(g.last[i]))
				case "sum":
					f = append(f, num(g.sum[i]))
				case "std":
					m := g.sum[i] / float64(g.n)
					f = append(f, num(math.Sqrt(math.Max(0, g.sumsq[i]/float64(g.n)-m*m))))
				}
			}
			sk := s.sketches[n]
			if sk == nil || !sk.has(ro.res) {
				continue
			}
			for _, p := range pcts {
				q, _ := quantileOf(p)
				bs := g.buckets[n]
				var tot int64
				for _, x := range bs {
					tot += x[1]
				}
				val := ""
				var cum int64
				for _, x := range bs {
					cum += x[1]
					if float64(cum) >= q*float64(tot) {
						val = digits(bucketValue(x[0], sk.gamma), 4) // a sketch is good to about 1%, so four digits say it all
						break
					}
				}
				f = append(f, val)
			}
		}
		if s.anomalies != "" {
			f = append(f, strconv.FormatInt(g.an, 10))
		}
		t.row(f...)
		shown++
	}

	// The line above the rows.
	var b strings.Builder
	fmt.Fprintf(&b, "Stream %s", s.name)
	if ft := filterText(fs); ft != "" {
		b.WriteString(", " + ft)
	}
	fmt.Fprintf(&b, ", from %s to %s: ", when(from), when(to))
	switch {
	case len(list) == 0:
		b.WriteString("no events in this range.\n")
		return b.String(), nil
	case all:
		fmt.Fprintf(&b, "one summary from %s of %s", plural(int64(len(rows.Rows)), "window", "windows"), durationText(ro.res))
	case every == ro.res:
		fmt.Fprintf(&b, "one row per %s window", durationText(every))
	default:
		fmt.Fprintf(&b, "one row per %s, added up from the %s windows", durationText(every), durationText(ro.res))
	}
	if len(by) < len(s.keys) {
		var merged []string
		for _, k := range s.keys {
			in := false
			for _, x := range by {
				in = in || x == k
			}
			if !in && !pinned[k] {
				merged = append(merged, k)
			}
		}
		if len(merged) > 0 {
			fmt.Fprintf(&b, ", every %s added together", strings.Join(merged, " and every "))
		}
	}
	b.WriteString(".")
	if minW > from || maxW+ro.res < to {
		fmt.Fprintf(&b, " The windows read cover %s to %s.", when(minW), when(maxW+ro.res))
	}
	b.WriteString("\n")
	b.WriteString(t.String())
	if len(list) > shown {
		fmt.Fprintf(&b, "(first %d of %d rows; narrow the range or raise limit)\n", shown, len(list))
	}
	if len(pcts) > 0 {
		acc := 0.0
		for _, n := range nums {
			acc = math.Max(acc, s.sketches[n].accuracy)
		}
		notes = append(notes, fmt.Sprintf("Percentiles are within %s%% of the exact value.", num(acc*100)))
	}
	if s.anomalies != "" {
		notes = append(notes, "unusual counts the events the anomaly rule flagged; get_kept with kind anomalies returns those kept whole.")
	}
	if len(notes) > 0 {
		b.WriteString(strings.Join(notes, " ") + "\n")
	}
	return b.String(), nil
}
