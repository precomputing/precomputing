// Package compile turns a policy into plain SQLite: tables for the windows and
// answers, one trigger per stream that keeps them current on every insert, and
// the statements that let old detail fade. The result runs in any SQLite 3.35
// or newer with the math functions switched on, which is the default build.
package compile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/policy"
)

// Output is the compiled policy.
type Output struct {
	// Schema creates the tables, views and triggers. Every statement is safe to run again.
	Schema string
	// Distill deletes detail that has outlived its policy. Bind :now to the time of the newest event.
	Distill string
}

// ZeroBucket is the sketch bucket for zero and negative values.
const ZeroBucket = -1000000

// Clip bounds how far one event moves the anomaly baseline, in standard deviations.
// Clipped deviations underestimate the variance by the factor huberK(Clip), which the
// update divides out, so the stored variance stays an unbiased estimate for normal data.
const Clip = 1.5

// huberK is E[min(Z², c²)] for a standard normal Z.
func huberK(c float64) float64 {
	tail := 0.5 * math.Erfc(c/math.Sqrt2)          // P(Z > c)
	pdf := math.Exp(-c*c/2) / math.Sqrt(2*math.Pi) // density at c
	return (1 - 2*tail) - 2*c*pdf + 2*c*c*tail
}

// Source compiles the text of a policy. name is used in comments only.
func Source(text, name string) (*Output, error) {
	pol, err := policy.Parse(text)
	if err != nil {
		return nil, err
	}
	return Compile(pol, name)
}

// Compile turns a parsed policy into SQL.
func Compile(pol *policy.Policy, name string) (*Output, error) {
	c := &compiler{pol: pol, objects: map[string]string{}}
	if err := c.names(); err != nil {
		return nil, err
	}
	var schema, triggers, distill strings.Builder
	fmt.Fprintf(&schema, "-- Compiled by Precomputing %s from %s.\n", version.Version, name)
	fmt.Fprintf(&schema, "-- File format %d. Every statement is safe to run again on the same file.\n", version.Format)
	fmt.Fprintf(&distill, "-- Distill for %s: run every few minutes with :now bound to the time of the newest event.\n", name)
	for _, s := range pol.Streams {
		c.stream(&schema, &triggers, &distill, s)
	}
	for _, g := range c.groups() {
		c.group(&schema, g)
	}
	for _, q := range pol.Quotas {
		c.quota(&schema, q)
	}
	if pol.Logs != nil {
		c.logs(&schema, pol.Logs)
	}
	if triggers.Len() > 0 {
		fmt.Fprintf(&schema, "\n-- The work done on every insert.\n%s", triggers.String())
	}
	c.meta(&schema, distill.String())
	return &Output{Schema: schema.String(), Distill: distill.String()}, nil
}

type compiler struct {
	pol     *policy.Policy
	objects map[string]string // name -> what made it, to catch clashes
	meta_   []object
}

type object struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Stream string `json:"stream"`
	Detail any    `json:"detail"`
}

func (c *compiler) claim(name, owner string, pos policy.Pos) error {
	if prev, taken := c.objects[name]; taken && prev != owner {
		return &policy.Error{Pos: pos, Msg: fmt.Sprintf("the name %q is needed for %s but is already used by %s", name, owner, prev)}
	}
	c.objects[name] = owner
	return nil
}

// names reserves every table and view name the policy will create and checks they do not clash.
func (c *compiler) names() error {
	for _, s := range c.pol.Streams {
		owner := "stream " + s.Name
		for _, n := range []string{s.Name, raw(s), raw(s) + "_ts", win(s), sample(s), base(s), anomaly(s), s.Name + "_ingest"} {
			if err := c.claim(n, owner, s.Pos); err != nil {
				return err
			}
		}
		if s.Exact {
			for _, n := range []string{ids(s), clock(s), refused(s)} {
				if err := c.claim(n, owner, s.Pos); err != nil {
					return err
				}
			}
		}
		for _, f := range c.sketched(s) {
			if err := c.claim(sk(s, f), owner, s.Pos); err != nil {
				return err
			}
		}
		cols := map[string]bool{}
		for _, f := range s.Numbers() {
			for _, suffix := range []string{"_sum", "_sumsq", "_min", "_max", "_first", "_last"} {
				cols[f+suffix] = true
			}
		}
		for _, k := range s.Keys {
			if cols[k.Name] {
				return &policy.Error{Pos: k.Pos, Msg: fmt.Sprintf("key %q clashes with a column the compiler adds for a value; rename the key", k.Name)}
			}
		}
	}
	for _, pc := range c.pol.Precomputes {
		if err := c.claim(pc.Name, "precompute "+pc.Name, pc.Pos); err != nil {
			return err
		}
	}
	for _, g := range c.groups() {
		if err := c.claim(g.table, "precompute state", g.pos); err != nil {
			return err
		}
	}
	for _, q := range c.pol.Quotas {
		for _, n := range []string{q.Name, q.Name + "_limit"} {
			if err := c.claim(n, "quota "+q.Name, q.Pos); err != nil {
				return err
			}
		}
	}
	return nil
}

// TemplatesTable holds the log templates the Engine learns, numbered in the order they appear.
const TemplatesTable = "_precomputing_templates"

// logs writes the table of log templates and records how lines are read.
func (c *compiler) logs(out *strings.Builder, lg *policy.Logs) {
	fmt.Fprintf(out, "\n-- Log templates. The Engine learns them from each line's message, separately for each service\n-- and level, and numbers them; a stream's template key is that number. initial holds the tokens\n-- a template started from.\n")
	fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY, service TEXT NOT NULL, level TEXT NOT NULL, template TEXT NOT NULL, initial TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, example TEXT NOT NULL);\n", TemplatesTable)
	masks := lg.Masks
	if masks == nil {
		masks = []string{}
	}
	c.add(object{"logs", "logs", "", map[string]any{
		"format": lg.Format, "fields": lg.Fields, "masks": masks, "similarity": lg.Similarity, "depth": lg.Depth,
		"templates_table": TemplatesTable,
	}})
}

// Table names.
func raw(s *policy.Stream) string          { return s.Name + "_raw" }
func win(s *policy.Stream) string          { return s.Name + "_win" }
func sk(s *policy.Stream, f string) string { return s.Name + "_" + f + "_sk" }
func sample(s *policy.Stream) string       { return s.Name + "_sample" }
func base(s *policy.Stream) string         { return s.Name + "_base" }
func anomaly(s *policy.Stream) string      { return s.Name + "_anomaly" }
func ids(s *policy.Stream) string          { return s.Name + "_ids" }
func clock(s *policy.Stream) string        { return s.Name + "_clock" }
func refused(s *policy.Stream) string      { return s.Name + "_refused" }

// idName is the stream's identifier column, if it has one.
func idName(s *policy.Stream) []string {
	if s.ID == nil {
		return nil
	}
	return []string{s.ID.Name}
}

// PeriodEnd is the SQL for the end of the period that the time x falls in. Hours and days use
// SQLite's integer division, months the calendar.
func PeriodEnd(per, x string) string {
	switch per {
	case "hour":
		return fmt.Sprintf("((CAST(%s AS INTEGER) / 3600 + 1) * 3600)", x)
	case "day":
		return fmt.Sprintf("((CAST(%s AS INTEGER) / 86400 + 1) * 86400)", x)
	}
	return fmt.Sprintf("CAST(strftime('%%s', %s, 'unixepoch', 'start of month', '+1 month') AS INTEGER)", x)
}

// periodStart is the SQL for the start of the period that the time x falls in.
func periodStart(per, x string) string {
	switch per {
	case "hour":
		return fmt.Sprintf("(CAST(%s AS INTEGER) / 3600 * 3600)", x)
	case "day":
		return fmt.Sprintf("(CAST(%s AS INTEGER) / 86400 * 86400)", x)
	}
	return fmt.Sprintf("CAST(strftime('%%s', %s, 'unixepoch', 'start of month') AS INTEGER)", x)
}

// sketched lists the values that have quantile sketches in some rollup, in stream order.
func (c *compiler) sketched(s *policy.Stream) []string {
	want := map[string]bool{}
	for _, r := range s.Rollups {
		for _, f := range r.Quantiles {
			want[f] = true
		}
	}
	var out []string
	for _, f := range s.Numbers() {
		if want[f] {
			out = append(out, f)
		}
	}
	return out
}

func num(x float64) string { return strconv.FormatFloat(x, 'g', -1, 64) }

func gamma(acc float64) float64 { return (1 + acc) / (1 - acc) }

func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func keyType(f *policy.Field) string {
	if f.Type == "integer" {
		return "INTEGER"
	}
	return "TEXT"
}

func valueType(f *policy.Field) string {
	if f.Type == "integer" {
		return "INTEGER"
	}
	return "REAL"
}

// newValue is the SQL for a value of the event being inserted; derived values are written out in full.
func newValue(s *policy.Stream, name string) string {
	for _, d := range s.Derived {
		if d.Name == name {
			return "(" + exprSQL(s, d.Expr) + ")"
		}
	}
	return "NEW." + name
}

func exprSQL(s *policy.Stream, e policy.Expr) string {
	switch x := e.(type) {
	case policy.Num:
		return x.Text
	case policy.Ref:
		return newValue(s, x.Name)
	case policy.Neg:
		return "-" + exprSQL(s, x.X)
	case policy.Binary:
		l, r := exprSQL(s, x.L), exprSQL(s, x.R)
		if x.Op == '/' {
			// Divide as real numbers, so 7 / 2 is 3.5 and not 3.
			l = "1.0 * " + l
		}
		return "(" + l + " " + string(x.Op) + " " + r + ")"
	}
	return "NULL"
}

func windowOf(res int64) string {
	return fmt.Sprintf("(CAST(NEW.ts AS INTEGER) / %d * %d)", res, res)
}

func keyNames(fs []*policy.Field) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func prefixed(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}

// baseMatch finds the baseline row of the event's keys.
func baseMatch(keys []string) string {
	if len(keys) == 0 {
		return "g = 1"
	}
	return tableMatch(keys, "")
}

// tableMatch finds the rows of a table with the event's keys; with no keys every row matches.
func tableMatch(keys []string, table string) string {
	if len(keys) == 0 {
		return "1"
	}
	var parts []string
	for _, k := range keys {
		col := k
		if table != "" {
			col = table + "." + k
		}
		parts = append(parts, col+" = NEW."+k)
	}
	return strings.Join(parts, " AND ")
}

func bucket(x string, acc float64) string {
	return fmt.Sprintf("CASE WHEN %s > 0 THEN CAST(ceil(ln(%s) / %s) AS INTEGER) ELSE %d END", x, x, num(math.Log(gamma(acc))), ZeroBucket)
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

func (c *compiler) stream(out, trig, distill *strings.Builder, s *policy.Stream) {
	keys := keyNames(s.Keys)
	vals := keyNames(s.Values)
	nums := s.Numbers()
	keyList := strings.Join(keys, ", ")
	idCol := idName(s)
	// fields are the columns of an event as it is inserted: time, identifier, keys, values, and
	// for a stream from logs the line itself.
	fields := append(append(append([]string{"ts"}, idCol...), keys...), vals...)
	if s.FromLogs {
		fields = append(fields, "line")
	}

	fmt.Fprintf(out, "\n-- Stream %s. Insert events with:\n--   INSERT INTO %s (%s) VALUES (%s);\n",
		s.Name, s.Name, strings.Join(fields, ", "),
		strings.TrimSuffix(strings.Repeat("?, ", len(fields)), ", "))

	// Raw tier and the view events are inserted into.
	var cols []string
	cols = append(cols, "ts INTEGER NOT NULL")
	if s.ID != nil {
		cols = append(cols, s.ID.Name+" "+keyType(s.ID)+" NOT NULL")
	}
	for _, k := range s.Keys {
		cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
	}
	for _, v := range s.Values {
		cols = append(cols, v.Name+" "+valueType(v)+" NOT NULL")
	}
	if s.FromLogs {
		cols = append(cols, "line TEXT NOT NULL")
	}
	fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s);\n", raw(s), strings.Join(cols, ", "))
	if s.RawKeep > 0 || s.RawUntilClosed {
		// Distill deletes the oldest raw events every few minutes; the index keeps that cheap.
		fmt.Fprintf(out, "CREATE INDEX IF NOT EXISTS %s_ts ON %s (ts);\n", raw(s), raw(s))
	}
	fmt.Fprintf(out, "CREATE VIEW IF NOT EXISTS %s AS SELECT %s FROM %s;\n", s.Name, strings.Join(fields, ", "), raw(s))
	detail := map[string]any{
		"keys": nonNil(keys), "values": nonNil(vals), "derived": derivedDetail(s), "raw_table": raw(s),
		"raw_keep_seconds": s.RawKeep, "insert": fmt.Sprintf("INSERT INTO %s (%s) VALUES (...)", s.Name, strings.Join(fields, ", ")),
	}
	if s.FromLogs {
		where := map[string]string{}
		for _, w := range s.Where {
			where[w.Field] = w.Value
		}
		detail["from"] = "logs"
		detail["where"] = where
	}
	if s.Exact {
		detail["exact"] = true
		detail["period"] = s.Period
		detail["close_seconds"] = s.Close
		detail["late_seconds"] = s.Late
		detail["raw_after_close_seconds"] = s.RawAfterClose
		detail["refused_table"] = refused(s)
		if s.ID != nil {
			detail["id"] = s.ID.Name
			detail["repeats_seconds"] = s.Repeats
			detail["ids_table"] = ids(s)
		}
	}
	c.add(object{s.Name, "stream", s.Name, detail})
	if s.Exact {
		// What an exact stream needs to refuse events: the identifiers it has seen, the time of
		// its newest event, and a count of what it refused and why.
		if s.ID != nil {
			fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s %s NOT NULL PRIMARY KEY, ts INTEGER NOT NULL) WITHOUT ROWID;\n", ids(s), s.ID.Name, keyType(s.ID))
			fmt.Fprintf(out, "CREATE INDEX IF NOT EXISTS %s_ts ON %s (ts);\n", ids(s), ids(s))
		}
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (g INTEGER PRIMARY KEY CHECK (g = 1), newest INTEGER NOT NULL);\n", clock(s))
		cols = []string{"reason TEXT NOT NULL"}
		for _, k := range s.Keys {
			cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
		}
		cols = append(cols, "n INTEGER NOT NULL", "PRIMARY KEY ("+joinNonEmpty("reason", keyList)+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", refused(s), strings.Join(cols, ", "))
	}

	// Window summaries.
	if len(s.Rollups) > 0 {
		cols = []string{"res INTEGER NOT NULL", "w INTEGER NOT NULL"}
		for _, k := range s.Keys {
			cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
		}
		cols = append(cols, "n INTEGER NOT NULL", "first_ts INTEGER NOT NULL", "last_ts INTEGER NOT NULL", "an INTEGER NOT NULL")
		for _, f := range nums {
			for _, suffix := range []string{"_sum", "_sumsq", "_min", "_max", "_first", "_last"} {
				cols = append(cols, f+suffix+" REAL NOT NULL")
			}
		}
		cols = append(cols, "PRIMARY KEY ("+joinNonEmpty("res, w", keyList)+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", win(s), strings.Join(cols, ", "))
		var res []map[string]any
		for _, r := range s.Rollups {
			res = append(res, map[string]any{"res_seconds": r.Res, "keep_seconds": r.Keep, "quantiles": nonNil(r.Quantiles)})
		}
		c.add(object{win(s), "windows", s.Name, map[string]any{"rollups": res, "numbers": nonNil(nums)}})
	}
	for _, f := range c.sketched(s) {
		cols = []string{"res INTEGER NOT NULL", "w INTEGER NOT NULL"}
		for _, k := range s.Keys {
			cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
		}
		cols = append(cols, "b INTEGER NOT NULL", "n INTEGER NOT NULL", "PRIMARY KEY ("+joinNonEmpty("res, w", keyList, "b")+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", sk(s, f), strings.Join(cols, ", "))
		var res []int64
		for _, r := range s.Rollups {
			for _, q := range r.Quantiles {
				if q == f {
					res = append(res, r.Res)
				}
			}
		}
		g := gamma(s.Accuracy)
		c.add(object{sk(s, f), "sketch", s.Name, map[string]any{
			"value": f, "res_seconds": res, "accuracy": s.Accuracy, "gamma": g, "zero_bucket": ZeroBucket,
			"estimate": fmt.Sprintf("2 * pow(%s, b) / (%s + 1)", num(g), num(g)),
		}})
	}
	if sm := s.Samples; sm != nil {
		cols = []string{"res INTEGER NOT NULL", "w INTEGER NOT NULL"}
		for _, k := range s.Keys {
			cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
		}
		cols = append(cols, "slot INTEGER NOT NULL", "ts INTEGER NOT NULL")
		for _, v := range s.Values {
			cols = append(cols, v.Name+" "+valueType(v)+" NOT NULL")
		}
		if s.FromLogs {
			cols = append(cols, "line TEXT NOT NULL")
		}
		cols = append(cols, "PRIMARY KEY ("+joinNonEmpty("res, w", keyList, "slot")+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", sample(s), strings.Join(cols, ", "))
		c.add(object{sample(s), "samples", s.Name, map[string]any{"per_seconds": sm.Per, "n": sm.N}})
	}
	if a := s.Anomalies; a != nil {
		prev := ""
		if a.Change {
			// The key's previous value, so each event is judged by its step from it.
			prev = ", prev REAL NOT NULL"
		}
		if len(keys) == 0 {
			fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (g INTEGER PRIMARY KEY CHECK (g = 1), n INTEGER NOT NULL, m REAL NOT NULL, var REAL NOT NULL%s);\n", base(s), prev)
		} else {
			cols = nil
			for _, k := range s.Keys {
				cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
			}
			cols = append(cols, "n INTEGER NOT NULL", "m REAL NOT NULL", "var REAL NOT NULL")
			if a.Change {
				cols = append(cols, "prev REAL NOT NULL")
			}
			cols = append(cols, "PRIMARY KEY ("+keyList+")")
			fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", base(s), strings.Join(cols, ", "))
		}
		cols = []string{"ts INTEGER NOT NULL"}
		for _, k := range s.Keys {
			cols = append(cols, k.Name+" "+keyType(k)+" NOT NULL")
		}
		for _, v := range s.Values {
			cols = append(cols, v.Name+" "+valueType(v)+" NOT NULL")
		}
		if s.FromLogs {
			cols = append(cols, "line TEXT NOT NULL")
		}
		cols = append(cols, "z REAL")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s);\n", anomaly(s), strings.Join(cols, ", "))
		detail := map[string]any{
			"value": a.Field, "log": a.Log, "z": a.Z, "memory": a.Memory, "warmup": a.Warmup,
			"keep": a.Keep, "per_seconds": a.Per, "baseline_table": base(s),
		}
		if a.Change {
			detail["change"] = true
		}
		c.add(object{anomaly(s), "anomalies", s.Name, detail})
	}

	// The trigger that does the work on every insert.
	fmt.Fprintf(trig, "CREATE TRIGGER IF NOT EXISTS %s_ingest INSTEAD OF INSERT ON %s\nBEGIN\n", s.Name, s.Name)
	var nulls []string
	for _, n := range fields {
		nulls = append(nulls, "NEW."+n+" IS NULL")
	}
	fmt.Fprintf(trig, "  SELECT RAISE(ABORT, %s) WHERE %s;\n",
		sqlString(fmt.Sprintf("%s: %s must not be null", s.Name, strings.Join(fields, ", "))),
		strings.Join(nulls, " OR "))
	if s.Exact {
		c.exactChecks(trig, s)
	}
	if s.RawKeep != 0 || s.RawUntilClosed {
		fmt.Fprintf(trig, "  INSERT INTO %s (%s) VALUES (%s);\n", raw(s), strings.Join(fields, ", "), strings.Join(prefixed("NEW.", fields), ", "))
	}

	flag := "0"
	if a := s.Anomalies; a != nil {
		x := newValue(s, a.Field)
		t, guard := x, ""
		if a.Log {
			t = "ln(" + x + ")"
			guard = x + " > 0 AND "
		}
		// j is what gets judged: the value itself, or with change its step from the key's previous value.
		j := t
		if a.Change {
			j = "(" + t + " - prev)"
		}
		z2 := num(a.Z * a.Z)
		flag = fmt.Sprintf("coalesce((SELECT CASE WHEN %sn >= %d AND (%s - m) * (%s - m) > %s * var THEN 1 ELSE 0 END FROM %s WHERE %s), 0)",
			guard, a.Warmup, j, j, z2, base(s), baseMatch(keys))
		exCols := append(append([]string{"ts"}, keys...), vals...)
		if s.FromLogs {
			exCols = append(exCols, "line")
		}
		fmt.Fprintf(trig, "  -- An unusual event is kept whole, judged against the baseline before this event updates it.\n")
		fmt.Fprintf(trig, "  INSERT INTO %s (%s, z)\n    SELECT %s, (%s - m) / sqrt(var) FROM %s\n    WHERE %s AND %sn >= %d AND (%s - m) * (%s - m) > %s * var\n      AND coalesce((SELECT an FROM %s WHERE res = %d AND w = %s AND %s), 0) < %d;\n",
			anomaly(s), strings.Join(exCols, ", "), strings.Join(prefixed("NEW.", exCols), ", "), j, base(s),
			baseMatch(keys), guard, a.Warmup, j, j, z2,
			win(s), a.Per, windowOf(a.Per), tableMatch(keys, win(s)), a.Keep)
	}

	for _, r := range s.Rollups {
		ins := []string{"res", "w"}
		vs := []string{strconv.FormatInt(r.Res, 10), windowOf(r.Res)}
		ins = append(ins, keys...)
		vs = append(vs, prefixed("NEW.", keys)...)
		ins = append(ins, "n", "first_ts", "last_ts", "an")
		vs = append(vs, "1", "NEW.ts", "NEW.ts", flag)
		var sets []string
		sets = append(sets, "n = n + 1")
		for _, f := range nums {
			x := newValue(s, f)
			ins = append(ins, f+"_sum", f+"_sumsq", f+"_min", f+"_max", f+"_first", f+"_last")
			vs = append(vs, x, x+" * "+x, x, x, x, x)
			sets = append(sets,
				fmt.Sprintf("%s_sum = %s_sum + excluded.%s_sum", f, f, f),
				fmt.Sprintf("%s_sumsq = %s_sumsq + excluded.%s_sumsq", f, f, f),
				fmt.Sprintf("%s_min = min(%s_min, excluded.%s_min)", f, f, f),
				fmt.Sprintf("%s_max = max(%s_max, excluded.%s_max)", f, f, f),
				fmt.Sprintf("%s_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.%s_first ELSE %s_first END", f, f, f),
				fmt.Sprintf("%s_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.%s_last ELSE %s_last END", f, f, f))
		}
		sets = append(sets, "first_ts = min(first_ts, excluded.first_ts)", "last_ts = max(last_ts, excluded.last_ts)", "an = an + excluded.an")
		fmt.Fprintf(trig, "  -- Rollup %s.\n  INSERT INTO %s (%s)\n    VALUES (%s)\n    ON CONFLICT (%s) DO UPDATE SET\n      %s;\n",
			policy.FormatDuration(r.Res), win(s), strings.Join(ins, ", "), strings.Join(vs, ", "),
			joinNonEmpty("res, w", keyList), strings.Join(sets, ",\n      "))
		for _, f := range r.Quantiles {
			fmt.Fprintf(trig, "  INSERT INTO %s (%s) VALUES (%s)\n    ON CONFLICT (%s) DO UPDATE SET n = n + 1;\n",
				sk(s, f), joinNonEmpty("res, w", keyList, "b, n"),
				joinNonEmpty(strconv.FormatInt(r.Res, 10)+", "+windowOf(r.Res), strings.Join(prefixed("NEW.", keys), ", "), bucket(newValue(s, f), s.Accuracy)+", 1"),
				joinNonEmpty("res, w", keyList, "b"))
		}
	}

	if sm := s.Samples; sm != nil {
		ins := append(append([]string{"res", "w"}, keys...), "slot", "ts")
		ins = append(ins, vals...)
		sel := []string{strconv.FormatInt(sm.Per, 10), windowOf(sm.Per)}
		sel = append(sel, prefixed("NEW.", keys)...)
		// Reservoir sampling with a fixed pseudo-random sequence, so the same events give the same samples.
		sel = append(sel, fmt.Sprintf("CASE WHEN n <= %d THEN n - 1 ELSE ((n * 1103515245 + 12345) %% 2147483648) %% %d END", sm.N, sm.N), "NEW.ts")
		sel = append(sel, prefixed("NEW.", vals)...)
		var sets []string
		sets = append(sets, "ts = excluded.ts")
		for _, v := range vals {
			sets = append(sets, v+" = excluded."+v)
		}
		if s.FromLogs {
			ins, sel, sets = append(ins, "line"), append(sel, "NEW.line"), append(sets, "line = excluded.line")
		}
		fmt.Fprintf(trig, "  -- A few whole events per %s window, chosen evenly.\n  INSERT INTO %s (%s)\n    SELECT %s FROM %s\n    WHERE res = %d AND w = %s AND %s AND (n <= %d OR ((n * 2654435761 + 97) %% 4294967296) %% n < %d)\n    ON CONFLICT (%s) DO UPDATE SET %s;\n",
			policy.FormatDuration(sm.Per), sample(s), strings.Join(ins, ", "), strings.Join(sel, ", "), win(s),
			sm.Per, windowOf(sm.Per), tableMatch(keys, win(s)), sm.N, sm.N,
			joinNonEmpty("res, w", keyList, "slot"), strings.Join(sets, ", "))
	}

	for _, g := range c.groups() {
		if g.stream == s {
			c.groupUpsert(trig, g)
		}
	}

	if a := s.Anomalies; a != nil {
		x := newValue(s, a.Field)
		t, cond := x, "1"
		if a.Log {
			t, cond = "ln("+x+")", x+" > 0"
		}
		alpha := num(1 / float64(a.Memory))
		target := keyList
		ins := joinNonEmpty(keyList, "n, m, var")
		sel := joinNonEmpty(strings.Join(prefixed("NEW.", keys), ", "), "1, "+t+", 0.0")
		if len(keys) == 0 {
			target, ins, sel = "g", "g, n, m, var", "1, 1, "+t+", 0.0"
		}
		// Unusual events do not move the baseline, and every other event moves it by at most
		// Clip standard deviations, so a long burst stays unusual, and counted, instead of
		// becoming the new normal. Slow drifts are learned.
		if a.Change {
			// The first event of a key only records its value. Every later one is judged by its step d from
			// the previous value, and the baseline learns the steps. The previous value always moves on, so
			// after a jump the next steps are judged from the new level.
			ins = joinNonEmpty(keyList, "n, m, var, prev")
			sel = joinNonEmpty(strings.Join(prefixed("NEW.", keys), ", "), "0, 0.0, 0.0, "+t)
			if len(keys) == 0 {
				ins, sel = "g, n, m, var, prev", "1, 0, 0.0, 0.0, "+t
			}
			d := "(excluded.prev - prev)"
			dc := fmt.Sprintf("min(max(%s - m, -%s * sqrt(var)), %s * sqrt(var))", d, num(Clip), num(Clip))
			flagged := fmt.Sprintf("n >= %d AND (%s - m) * (%s - m) > %s * var", a.Warmup, d, d, num(a.Z*a.Z))
			fmt.Fprintf(trig, "  -- The baseline of steps: a plain mean and variance while warming up, then an exponentially weighted one\n  -- that each step moves by at most %s standard deviations. Steps judged unusual above leave it unchanged.\n", num(Clip))
			fmt.Fprintf(trig, "  INSERT INTO %s (%s) SELECT %s WHERE %s\n    ON CONFLICT (%s) DO UPDATE SET\n      n = CASE WHEN %s THEN n ELSE n + 1 END,\n      m = CASE WHEN %s THEN m WHEN n < %d THEN m + (%s - m) / (n + 1) ELSE m + %s * %s END,\n      var = CASE WHEN %s THEN var WHEN n < %d THEN (n * var + (%s - m) * (%s - (m + (%s - m) / (n + 1)))) / (n + 1) ELSE (1 - %s) * (var + %s * %s * %s / %s) END,\n      prev = excluded.prev;\n",
				base(s), ins, sel, cond, target,
				flagged,
				flagged, a.Warmup, d, alpha, dc,
				flagged, a.Warmup, d, d, d, alpha, alpha, dc, dc, num(huberK(Clip)))
		} else {
			dc := fmt.Sprintf("min(max(excluded.m - m, -%s * sqrt(var)), %s * sqrt(var))", num(Clip), num(Clip))
			fmt.Fprintf(trig, "  -- The baseline: a plain mean and variance while warming up, then an exponentially weighted one\n  -- that each event moves by at most %s standard deviations. Events judged unusual above leave it unchanged.\n", num(Clip))
			fmt.Fprintf(trig, "  INSERT INTO %s (%s) SELECT %s WHERE %s AND %s = 0\n    ON CONFLICT (%s) DO UPDATE SET\n      n = n + 1,\n      m = CASE WHEN n < %d THEN m + (excluded.m - m) / (n + 1) ELSE m + %s * %s END,\n      var = CASE WHEN n < %d THEN (n * var + (excluded.m - m) * (excluded.m - (m + (excluded.m - m) / (n + 1)))) / (n + 1) ELSE (1 - %s) * (var + %s * %s * %s / %s) END;\n",
				base(s), ins, sel, cond, flag, target, a.Warmup, alpha, dc, a.Warmup, alpha, alpha, dc, dc, num(huberK(Clip)))
		}
	}
	fmt.Fprintf(trig, "END;\n")

	// Distill: detail fades on schedule; anomalies and precomputed answers stay.
	fmt.Fprintf(distill, "\n-- Stream %s.\n", s.Name)
	if s.RawKeep > 0 {
		fmt.Fprintf(distill, "DELETE FROM %s WHERE ts < :now - %d;\n", raw(s), s.RawKeep)
	}
	if s.RawUntilClosed {
		// An event goes once its period has been closed for the time the policy gives: every
		// period that ended before :now - close - that time.
		fmt.Fprintf(distill, "DELETE FROM %s WHERE ts < %s;\n", raw(s), periodStart(s.Period, fmt.Sprintf(":now - %d", s.Close+s.RawAfterClose)))
	}
	if s.Exact && s.ID != nil {
		fmt.Fprintf(distill, "DELETE FROM %s WHERE ts < :now - %d;\n", ids(s), s.Repeats)
	}
	for _, r := range s.Rollups {
		if r.Keep < 0 {
			continue
		}
		fmt.Fprintf(distill, "DELETE FROM %s WHERE res = %d AND w <= :now - %d;\n", win(s), r.Res, r.Keep+r.Res)
		for _, f := range r.Quantiles {
			fmt.Fprintf(distill, "DELETE FROM %s WHERE res = %d AND w <= :now - %d;\n", sk(s, f), r.Res, r.Keep+r.Res)
		}
		if s.Samples != nil && s.Samples.Per == r.Res {
			fmt.Fprintf(distill, "DELETE FROM %s WHERE res = %d AND w <= :now - %d;\n", sample(s), r.Res, r.Keep+r.Res)
		}
	}
}

// exactChecks writes the start of an exact stream's trigger: an event is refused, and counted
// by reason, when it is later than the policy allows, when its period has closed, or when its
// identifier has been seen. A refused event changes nothing else.
func (c *compiler) exactChecks(trig *strings.Builder, s *policy.Stream) {
	keys := keyNames(s.Keys)
	ins := joinNonEmpty("reason", strings.Join(keys, ", "), "n")
	target := joinNonEmpty("reason", strings.Join(keys, ", "))
	newest := fmt.Sprintf("(SELECT newest FROM %s WHERE g = 1)", clock(s))
	refuse := func(reason, cond string) {
		sel := joinNonEmpty(sqlString(reason), strings.Join(prefixed("NEW.", keys), ", "), "1")
		fmt.Fprintf(trig, "  INSERT INTO %s (%s) SELECT %s WHERE %s\n    ON CONFLICT (%s) DO UPDATE SET n = n + 1;\n", refused(s), ins, sel, cond, target)
		fmt.Fprintf(trig, "  SELECT RAISE(IGNORE) WHERE %s;\n", cond)
	}
	fmt.Fprintf(trig, "  -- An exact stream refuses what would change a closed period or count an event twice.\n")
	if s.Late > 0 {
		refuse("late", fmt.Sprintf("NEW.ts < %s - %d", newest, s.Late))
	}
	refuse("closed", fmt.Sprintf("%s >= %s + %d", newest, PeriodEnd(s.Period, "NEW.ts"), s.Close))
	if s.ID != nil {
		refuse("repeat", fmt.Sprintf("EXISTS (SELECT 1 FROM %s WHERE %s = NEW.%s)", ids(s), s.ID.Name, s.ID.Name))
		fmt.Fprintf(trig, "  INSERT INTO %s (%s, ts) VALUES (NEW.%s, NEW.ts);\n", ids(s), s.ID.Name, s.ID.Name)
	}
	fmt.Fprintf(trig, "  INSERT INTO %s (g, newest) VALUES (1, NEW.ts)\n    ON CONFLICT (g) DO UPDATE SET newest = max(newest, excluded.newest);\n", clock(s))
}

// quota writes a quota: a table of limits and a view of use against them.
func (c *compiler) quota(out *strings.Builder, q *policy.Quota) {
	var pc *policy.Precompute
	for _, x := range c.pol.Precomputes {
		if x.Name == q.Precompute {
			pc = x
		}
	}
	s := c.pol.Stream(pc.Stream)
	var cols, by []string
	for _, k := range pc.By {
		for _, f := range s.Keys {
			if f.Name == k {
				cols = append(cols, k+" "+keyType(f)+" NOT NULL")
				by = append(by, k)
			}
		}
	}
	limit := q.Name + "_limit"
	fmt.Fprintf(out, "\n-- Quota %s on %s. Put the limits in %s; the view shows each period's use against them.\n", q.Name, pc.Name, limit)
	if len(by) == 0 {
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (g INTEGER PRIMARY KEY CHECK (g = 1), lim REAL NOT NULL);\n", limit)
		fmt.Fprintf(out, "CREATE VIEW IF NOT EXISTS %s AS\n  SELECT u.period, u.value AS used, l.lim, l.lim - u.value AS remaining, u.value >= l.lim AS reached\n  FROM %s l, %s u;\n", q.Name, limit, pc.Name)
	} else {
		cols = append(cols, "lim REAL NOT NULL", "PRIMARY KEY ("+strings.Join(by, ", ")+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", limit, strings.Join(cols, ", "))
		fmt.Fprintf(out, "CREATE VIEW IF NOT EXISTS %s AS\n  SELECT %s, u.period, u.value AS used, l.lim, l.lim - u.value AS remaining, u.value >= l.lim AS reached\n  FROM %s l JOIN %s u USING (%s);\n",
			q.Name, strings.Join(prefixed("l.", by), ", "), limit, pc.Name, strings.Join(by, ", "))
	}
	c.add(object{q.Name, "quota", s.Name, map[string]any{"precompute": pc.Name, "limit_table": limit, "by": nonNil(by), "per": pc.Per}})
}

func derivedDetail(s *policy.Stream) []map[string]string {
	out := []map[string]string{}
	for _, d := range s.Derived {
		out = append(out, map[string]string{"name": d.Name, "sql": exprSQL(s, d.Expr)})
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func (c *compiler) add(o object) { c.meta_ = append(c.meta_, o) }

// A group is the state behind one or more precomputes that read the same stream with the same by and per.
type group struct {
	stream  *policy.Stream
	by      []string
	per     string
	field   string // for quantile groups
	quant   bool
	table   string
	pcs     []*policy.Precompute
	pos     policy.Pos
	needSum map[string]bool
	needMin map[string]bool
	needMax map[string]bool
	needFst map[string]bool
	needLst map[string]bool
}

func (c *compiler) groups() []*group {
	var out []*group
	find := func(s *policy.Stream, by []string, per, field string, quant bool) *group {
		key := strings.Join(by, ",")
		for _, g := range out {
			if g.stream == s && strings.Join(g.by, ",") == key && g.per == per && g.quant == quant && (!quant || g.field == field) {
				return g
			}
		}
		name := "_pc_" + s.Name
		if quant {
			name = "_pcq_" + s.Name + "_" + field
		}
		if len(by) > 0 {
			name += "_by_" + strings.Join(by, "_")
		}
		if per != "" {
			name += "_per_" + per
		}
		g := &group{stream: s, by: by, per: per, field: field, quant: quant, table: name,
			needSum: map[string]bool{}, needMin: map[string]bool{}, needMax: map[string]bool{}, needFst: map[string]bool{}, needLst: map[string]bool{}}
		out = append(out, g)
		return g
	}
	for _, pc := range c.pol.Precomputes {
		s := c.pol.Stream(pc.Stream)
		if s == nil {
			continue
		}
		g := find(s, pc.By, pc.Per, pc.Field, pc.Func == "quantile")
		if len(g.pcs) == 0 {
			g.pos = pc.Pos
		}
		g.pcs = append(g.pcs, pc)
		switch pc.Func {
		case "sum", "avg":
			g.needSum[pc.Field] = true
		case "min":
			g.needMin[pc.Field] = true
		case "max":
			g.needMax[pc.Field] = true
		case "first":
			g.needFst[pc.Field] = true
		case "last":
			g.needLst[pc.Field] = true
		}
	}
	return out
}

func periodOf(per string) string {
	f := map[string]string{"hour": "%Y-%m-%dT%H", "day": "%Y-%m-%d", "month": "%Y-%m"}[per]
	return fmt.Sprintf("strftime('%s', NEW.ts, 'unixepoch')", f)
}

func (g *group) keyCols() []string {
	cols := append([]string{}, g.by...)
	if g.per != "" {
		cols = append(cols, "period")
	}
	return cols
}

func (g *group) has(m map[string]bool) []string {
	var out []string
	for _, f := range g.stream.Numbers() {
		if m[f] {
			out = append(out, f)
		}
	}
	return out
}

func (c *compiler) group(out *strings.Builder, g *group) {
	s := g.stream
	keyCols := g.keyCols()
	var cols []string
	for _, k := range g.by {
		for _, f := range s.Keys {
			if f.Name == k {
				cols = append(cols, k+" "+keyType(f)+" NOT NULL")
			}
		}
	}
	if g.per != "" {
		cols = append(cols, "period TEXT NOT NULL")
	}
	pk := strings.Join(keyCols, ", ")
	if len(keyCols) == 0 {
		cols = append([]string{"g INTEGER PRIMARY KEY CHECK (g = 1)"}, cols...)
	}
	if g.quant {
		fmt.Fprintf(out, "\n-- Precomputed quantiles of %s.%s%s%s.\n", s.Name, g.field, byText(g.by), perText(g.per))
	} else {
		fmt.Fprintf(out, "\n-- Precomputed answers from stream %s%s%s.\n", s.Name, byText(g.by), perText(g.per))
	}
	if g.quant {
		cols = append(cols, "b INTEGER NOT NULL", "n INTEGER NOT NULL", "PRIMARY KEY ("+joinNonEmpty(pk, "b")+")")
		if len(keyCols) == 0 {
			cols = cols[1:] // b alone is the key
		}
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", g.table, strings.Join(cols, ", "))
		gm := gamma(s.Accuracy)
		for _, pc := range g.pcs {
			part := ""
			if pk != "" {
				part = "PARTITION BY " + pk + " "
			}
			sel := joinNonEmpty(pk, fmt.Sprintf("CASE WHEN min(b) = %d THEN 0.0 ELSE 2 * pow(%s, min(b)) / (%s + 1) END AS value", ZeroBucket, num(gm), num(gm)))
			groupBy := ""
			if pk != "" {
				groupBy = " GROUP BY " + pk
			}
			fmt.Fprintf(out, "CREATE VIEW IF NOT EXISTS %s AS\n  WITH c AS (SELECT %s, sum(n) OVER (%sORDER BY b ROWS UNBOUNDED PRECEDING) AS cum, sum(n) OVER (%s) AS tot FROM %s)\n  SELECT %s FROM c WHERE cum >= %s * tot%s;\n",
				pc.Name, joinNonEmpty(pk, "b"), part, strings.TrimSpace(part), g.table, sel, num(pc.Q), groupBy)
			c.add(object{pc.Name, "precompute", s.Name, map[string]any{
				"function": pc.FuncAs, "quantile": pc.Q, "value": pc.Field, "by": nonNil(g.by), "per": nilIfEmpty(g.per),
				"state": g.table, "accuracy": s.Accuracy, "gamma": gm,
			}})
		}
		return
	}
	cols = append(cols, "n INTEGER NOT NULL")
	for _, f := range g.has(g.needSum) {
		cols = append(cols, f+"_sum REAL NOT NULL")
	}
	for _, f := range g.has(g.needMin) {
		cols = append(cols, f+"_min REAL NOT NULL")
	}
	for _, f := range g.has(g.needMax) {
		cols = append(cols, f+"_max REAL NOT NULL")
	}
	for _, f := range g.has(g.needFst) {
		cols = append(cols, f+"_first REAL NOT NULL")
	}
	for _, f := range g.has(g.needLst) {
		cols = append(cols, f+"_last REAL NOT NULL")
	}
	if len(g.needFst) > 0 {
		cols = append(cols, "first_ts INTEGER NOT NULL")
	}
	if len(g.needLst) > 0 {
		cols = append(cols, "last_ts INTEGER NOT NULL")
	}
	if len(keyCols) > 0 {
		cols = append(cols, "PRIMARY KEY ("+pk+")")
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s) WITHOUT ROWID;\n", g.table, strings.Join(cols, ", "))
	} else {
		fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS %s (%s);\n", g.table, strings.Join(cols, ", "))
	}
	for _, pc := range g.pcs {
		var expr string
		switch pc.Func {
		case "count":
			expr = "n"
		case "sum":
			expr = pc.Field + "_sum"
		case "avg":
			expr = pc.Field + "_sum / n"
		default:
			expr = pc.Field + "_" + pc.Func
		}
		fmt.Fprintf(out, "CREATE VIEW IF NOT EXISTS %s AS SELECT %s FROM %s;\n", pc.Name, joinNonEmpty(pk, expr+" AS value"), g.table)
		c.add(object{pc.Name, "precompute", s.Name, map[string]any{
			"function": pc.FuncAs, "value": nilIfEmpty(pc.Field), "by": nonNil(g.by), "per": nilIfEmpty(g.per), "state": g.table,
		}})
	}
}

func byText(by []string) string {
	if len(by) == 0 {
		return ""
	}
	return " by " + strings.Join(by, ", ")
}

func perText(per string) string {
	if per == "" {
		return ""
	}
	return " per " + per
}

func (c *compiler) groupUpsert(out *strings.Builder, g *group) {
	s := g.stream
	keyCols := g.keyCols()
	var ins, vs []string
	if len(keyCols) == 0 && !g.quant {
		ins, vs = append(ins, "g"), append(vs, "1")
	}
	for _, k := range g.by {
		ins, vs = append(ins, k), append(vs, "NEW."+k)
	}
	if g.per != "" {
		ins, vs = append(ins, "period"), append(vs, periodOf(g.per))
	}
	target := strings.Join(keyCols, ", ")
	if g.quant {
		x := newValue(s, g.field)
		ins, vs = append(ins, "b", "n"), append(vs, bucket(x, s.Accuracy), "1")
		fmt.Fprintf(out, "  -- Precomputed quantiles of %s%s%s.\n  INSERT INTO %s (%s) VALUES (%s)\n    ON CONFLICT (%s) DO UPDATE SET n = n + 1;\n",
			g.field, byText(g.by), perText(g.per), g.table, strings.Join(ins, ", "), strings.Join(vs, ", "), joinNonEmpty(target, "b"))
		return
	}
	if target == "" {
		target = "g"
	}
	ins, vs = append(ins, "n"), append(vs, "1")
	sets := []string{"n = n + 1"}
	for _, f := range g.has(g.needSum) {
		ins, vs = append(ins, f+"_sum"), append(vs, newValue(s, f))
		sets = append(sets, fmt.Sprintf("%s_sum = %s_sum + excluded.%s_sum", f, f, f))
	}
	for _, f := range g.has(g.needMin) {
		ins, vs = append(ins, f+"_min"), append(vs, newValue(s, f))
		sets = append(sets, fmt.Sprintf("%s_min = min(%s_min, excluded.%s_min)", f, f, f))
	}
	for _, f := range g.has(g.needMax) {
		ins, vs = append(ins, f+"_max"), append(vs, newValue(s, f))
		sets = append(sets, fmt.Sprintf("%s_max = max(%s_max, excluded.%s_max)", f, f, f))
	}
	for _, f := range g.has(g.needFst) {
		ins, vs = append(ins, f+"_first"), append(vs, newValue(s, f))
		sets = append(sets, fmt.Sprintf("%s_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.%s_first ELSE %s_first END", f, f, f))
	}
	for _, f := range g.has(g.needLst) {
		ins, vs = append(ins, f+"_last"), append(vs, newValue(s, f))
		sets = append(sets, fmt.Sprintf("%s_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.%s_last ELSE %s_last END", f, f, f))
	}
	if len(g.needFst) > 0 {
		ins, vs = append(ins, "first_ts"), append(vs, "NEW.ts")
		sets = append(sets, "first_ts = min(first_ts, excluded.first_ts)")
	}
	if len(g.needLst) > 0 {
		ins, vs = append(ins, "last_ts"), append(vs, "NEW.ts")
		sets = append(sets, "last_ts = max(last_ts, excluded.last_ts)")
	}
	fmt.Fprintf(out, "  -- Precomputed answers%s%s.\n  INSERT INTO %s (%s) VALUES (%s)\n    ON CONFLICT (%s) DO UPDATE SET %s;\n",
		byText(g.by), perText(g.per), g.table, strings.Join(ins, ", "), strings.Join(vs, ", "), target, strings.Join(sets, ", "))
}

// marshal writes JSON with <, > and & as they are, since the file is not HTML.
func marshal(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

func (c *compiler) meta(out *strings.Builder, distill string) {
	fmt.Fprintf(out, "\n-- What this file holds, for any tool that opens it.\n")
	fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);\n")
	fmt.Fprintf(out, "INSERT OR REPLACE INTO _precomputing (key, value) VALUES\n  ('format', %s),\n  ('compiler', %s),\n  ('policy', %s),\n  ('distill', %s);\n",
		sqlString(strconv.Itoa(version.Format)), sqlString("precomputing "+version.Version),
		sqlString(c.pol.Source), sqlString(distill))
	fmt.Fprintf(out, "CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);\n")
	var rows []string
	for _, o := range c.meta_ {
		rows = append(rows, fmt.Sprintf("  (%s, %s, %s, %s)", sqlString(o.Name), sqlString(o.Kind), sqlString(o.Stream), sqlString(marshal(o.Detail))))
	}
	if len(rows) > 0 {
		fmt.Fprintf(out, "INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES\n%s;\n", strings.Join(rows, ",\n"))
	}
}
