package mcp

import (
	"fmt"
	"strings"
)

// maxKeyValues is how many values of a key describe_file names, the busiest first.
const maxKeyValues = 12

func first(rows *Rows) []any {
	if rows == nil || len(rows.Rows) == 0 {
		return nil
	}
	return rows.Rows[0]
}

func intAt(row []any, i int) int64 {
	if i >= len(row) {
		return 0
	}
	n, _ := asInt(row[i])
	return n
}

// newest is the time of the newest event the file holds, from the finest windows or the raw events.
func newest(r Reader, c *catalog) (int64, error) {
	var t int64
	for _, s := range c.streams {
		if len(s.rollups) > 0 {
			for _, ro := range s.rollups {
				row, err := r.Read(fmt.Sprintf("SELECT max(last_ts) FROM %s WHERE res = ? AND w = (SELECT max(w) FROM %s WHERE res = ?)", quoteName(s.win), quoteName(s.win)), []any{ro.res, ro.res}, 0)
				if err != nil {
					return 0, err
				}
				if v := intAt(first(row), 0); v > t {
					t = v
				}
			}
		}
		row, err := r.Read(fmt.Sprintf("SELECT max(ts) FROM %s", quoteName(s.rawTable)), nil, 0)
		if err != nil {
			return 0, err
		}
		if v := intAt(first(row), 0); v > t {
			t = v
		}
	}
	return t, nil
}

func describeFile(r Reader, a args) (string, error) {
	c, err := loadCatalog(r)
	if err != nil {
		return "", err
	}
	now, err := newest(r, c)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "A Precomputing file (format %s, written by %s). Times are UTC.", c.format, c.compiler)
	if now > 0 {
		fmt.Fprintf(&b, " The newest event is at %s.", when(now))
	}
	b.WriteString("\n")
	for _, s := range c.streams {
		if err := describeStream(r, c, s, &b); err != nil {
			return "", err
		}
	}
	var pcs, quotas []*answer
	for _, an := range c.answers {
		if an.kind == "quota" {
			quotas = append(quotas, an)
		} else {
			pcs = append(pcs, an)
		}
	}
	if len(pcs) > 0 {
		b.WriteString("\nPrecomputes, kept current as events arrive (read them with get_answer):\n")
		for _, an := range pcs {
			line := an.describe()
			if an.accuracy > 0 {
				line += fmt.Sprintf(", within %s%%", num(an.accuracy*100))
			}
			b.WriteString("- " + line + "\n")
		}
	}
	if len(quotas) > 0 {
		b.WriteString("\nQuotas (get_answer):\n")
		for _, an := range quotas {
			b.WriteString("- " + an.describe() + "\n")
		}
	}
	if len(c.refusals) > 0 {
		b.WriteString("\nRefused events, counted by reason and key (get_answer):\n")
		for _, t := range c.refusals {
			rows, err := r.Read(fmt.Sprintf("SELECT reason, sum(n) FROM %s GROUP BY reason ORDER BY reason", quoteName(t)), nil, 0)
			if err != nil {
				return "", err
			}
			var parts []string
			for _, row := range rows.Rows {
				parts = append(parts, fmt.Sprintf("%s %s", cell(row[0]), cell(row[1])))
			}
			counts := "none so far"
			if len(parts) > 0 {
				counts = strings.Join(parts, ", ")
			}
			fmt.Fprintf(&b, "- %s: %s\n", t, counts)
		}
	}
	if c.templates != "" {
		row, err := r.Read(fmt.Sprintf("SELECT count(*), min(first_ts), max(first_ts) FROM %s", quoteName(c.templates)), nil, 0)
		if err != nil {
			return "", err
		}
		x := first(row)
		fmt.Fprintf(&b, "\nLog templates: %d learned", intAt(x, 0))
		if intAt(x, 0) > 0 {
			fmt.Fprintf(&b, ", first seen from %s to %s", when(intAt(x, 1)), when(intAt(x, 2)))
		}
		fmt.Fprintf(&b, ". Lines are read as %s. get_kept with kind templates lists them, and from a time lists the new ones.\n", strings.Join(wrapAngles(c.logFields), " "))
	}
	if len(c.others) > 0 {
		b.WriteString("\nThe file's other tables and views (get_answer, or query):\n")
		for _, o := range c.others {
			fmt.Fprintf(&b, "- %s (%s): %s\n", o.name, o.kind, strings.Join(o.columns, ", "))
		}
	}
	return b.String(), nil
}

func wrapAngles(fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = "<" + f + ">"
	}
	return out
}

func describeStream(r Reader, c *catalog, s *stream, b *strings.Builder) error {
	fmt.Fprintf(b, "\nStream %s", s.name)
	var notes []string
	if s.fromLogs {
		if len(s.where) == 0 {
			notes = append(notes, "every log line")
		} else {
			var ws []string
			for _, f := range c.logFields {
				if v, ok := s.where[f]; ok {
					ws = append(ws, fmt.Sprintf("%s = %s", f, v))
				}
			}
			notes = append(notes, "log lines where "+strings.Join(ws, " and "))
		}
	}
	if s.exact {
		n := "exact"
		if s.id != "" {
			n += ", each event counted once by its " + s.id
		}
		n += ", periods of a " + s.period
		notes = append(notes, n)
	}
	if len(notes) > 0 {
		fmt.Fprintf(b, " (%s)", strings.Join(notes, "; "))
	}
	b.WriteString(":")
	if len(s.keys) > 0 {
		fmt.Fprintf(b, " keys %s;", strings.Join(s.keys, ", "))
	}
	if len(s.values) > 0 {
		fmt.Fprintf(b, " values %s;", strings.Join(s.values, ", "))
	} else {
		b.WriteString(" no values, events are counted;")
	}
	if len(s.derived) > 0 {
		fmt.Fprintf(b, " derived %s;", strings.Join(s.derived, ", "))
	}
	b.WriteString("\n")

	// The span and the number of events, from the coarsest windows, which are kept the longest.
	var base int64
	if len(s.rollups) > 0 {
		base = s.rollups[len(s.rollups)-1].res
		row, err := r.Read(fmt.Sprintf("SELECT sum(n), min(first_ts), max(last_ts) FROM %s WHERE res = ?", quoteName(s.win)), []any{base}, 0)
		if err != nil {
			return err
		}
		x := first(row)
		if intAt(x, 0) > 0 {
			fmt.Fprintf(b, "  %d events from %s to %s.\n", intAt(x, 0), when(intAt(x, 1)), when(intAt(x, 2)))
		} else {
			b.WriteString("  No events yet.\n")
		}
		var ws []string
		for _, ro := range s.rollups {
			row, err := r.Read(fmt.Sprintf("SELECT count(*), min(w), max(w) FROM %s WHERE res = ?", quoteName(s.win)), []any{ro.res}, 0)
			if err != nil {
				return err
			}
			x := first(row)
			w := fmt.Sprintf("%s kept %s", durationText(ro.res), durationText(ro.keep))
			var sk []string
			for _, q := range ro.quantiles {
				sk = append(sk, q)
			}
			if len(sk) > 0 {
				w += " with percentiles of " + strings.Join(sk, ", ")
			}
			if intAt(x, 0) == 0 {
				w += " (none left)"
			} else {
				w += fmt.Sprintf(" (%s to %s)", when(intAt(x, 1)), when(intAt(x, 2)+ro.res))
			}
			ws = append(ws, w)
		}
		fmt.Fprintf(b, "  Windows (get_windows): %s.\n", strings.Join(ws, "; "))
	}

	// What is kept whole.
	var kept []string
	if s.rawKeep != 0 || s.exact {
		row, err := r.Read(fmt.Sprintf("SELECT count(*), min(ts), max(ts) FROM %s", quoteName(s.rawTable)), nil, 0)
		if err != nil {
			return err
		}
		x := first(row)
		rule := "kept " + durationText(s.rawKeep)
		if s.exact {
			rule = fmt.Sprintf("kept until their %s has been closed %s", s.period, durationText(s.rawAfter))
		}
		k := "raw events " + rule
		if intAt(x, 0) == 0 {
			k += ": none now"
		} else {
			k += fmt.Sprintf(": %d, %s to %s", intAt(x, 0), when(intAt(x, 1)), when(intAt(x, 2)))
		}
		kept = append(kept, k)
	}
	if s.samples != "" {
		row, err := r.Read(fmt.Sprintf("SELECT count(*) FROM %s", quoteName(s.samples)), nil, 0)
		if err != nil {
			return err
		}
		kept = append(kept, fmt.Sprintf("%s per %s per key: %d", plural(s.sampleN, "sample", "samples"), durationText(s.samplePer), intAt(first(row), 0)))
	}
	if s.anomalies != "" {
		row, err := r.Read(fmt.Sprintf("SELECT count(*) FROM %s", quoteName(s.anomalies)), nil, 0)
		if err != nil {
			return err
		}
		d := s.anomaly
		rule := stringOf(d["value"])
		if d["log"] == true {
			rule = "log " + rule
		}
		what := "values"
		if d["change"] == true {
			what = "steps"
			rule += " change"
		}
		kept = append(kept, fmt.Sprintf("unusual %s (%s, z > %s, at most %d per key per %s): %d",
			what, rule, num(floatOf(d["z"])), intOf(d["keep"]), durationText(intOf(d["per_seconds"])), intAt(first(row), 0)))
	}
	if len(kept) > 0 {
		fmt.Fprintf(b, "  Kept whole (get_kept): %s.\n", strings.Join(kept, "; "))
	}

	// The values each key takes, busiest first.
	for _, k := range s.keys {
		table, where, weight, args := quoteName(s.rawTable), "", "count(*)", []any(nil)
		if len(s.rollups) > 0 {
			table, where, weight, args = quoteName(s.win), " WHERE res = ?", "sum(n)", []any{base}
		}
		cnt, err := r.Read(fmt.Sprintf("SELECT count(DISTINCT %s) FROM %s%s", quoteName(k), table, where), args, 0)
		if err != nil {
			return err
		}
		total := intAt(first(cnt), 0)
		if k == "template" && s.intKey[k] && c.templates != "" {
			fmt.Fprintf(b, "  %s: %d template ids (get_kept kind templates says what each is)\n", k, total)
			continue
		}
		rows, err := r.Read(fmt.Sprintf("SELECT %s, %s FROM %s%s GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT %d", quoteName(k), weight, table, where, maxKeyValues), args, 0)
		if err != nil {
			return err
		}
		var vs []string
		for _, row := range rows.Rows {
			vs = append(vs, cell(row[0]))
		}
		more := ""
		if total > int64(len(vs)) {
			more = fmt.Sprintf(", and %d more", total-int64(len(vs)))
		}
		fmt.Fprintf(b, "  %s: %s%s\n", k, strings.Join(vs, ", "), more)
	}
	return nil
}

func floatOf(v any) float64 {
	f, _ := v.(float64)
	return f
}
