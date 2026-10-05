package mcp

import (
	"fmt"
	"strings"
)

// likePattern makes a LIKE pattern that finds text anywhere, with % and _ taken literally.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func getKept(r Reader, a args) (string, error) {
	kind, err := a.str("kind", true)
	if err != nil {
		return "", err
	}
	kind = strings.ToLower(kind)
	limit, err := a.integer("limit", 20, 1, 200)
	if err != nil {
		return "", err
	}
	order, err := a.str("order", false)
	if err != nil {
		return "", err
	}
	desc, unusual := true, false
	switch strings.ToLower(order) {
	case "", "newest":
	case "oldest":
		desc = false
	case "unusual":
		unusual = true
		if kind != "anomalies" {
			return "", fmt.Errorf("get_kept: order unusual sorts anomalies by their z-score; use newest or oldest for %s", kind)
		}
	default:
		return "", fmt.Errorf("get_kept: order is newest, oldest or unusual")
	}
	from, hasFrom, err := a.when("from")
	if err != nil {
		return "", err
	}
	to, hasTo, err := a.when("to")
	if err != nil {
		return "", err
	}
	contains, err := a.str("contains", false)
	if err != nil {
		return "", err
	}
	c, err := loadCatalog(r)
	if err != nil {
		return "", err
	}
	if kind == "templates" {
		return getTemplates(r, a, c, from, hasFrom, to, hasTo, contains, desc, limit)
	}
	name, err := a.str("stream", true)
	if err != nil {
		return "", fmt.Errorf("get_kept: kind %s needs a stream", kind)
	}
	s, err := c.stream(name)
	if err != nil {
		return "", fmt.Errorf("get_kept: %v", err)
	}
	var tbl, what string
	switch kind {
	case "raw":
		if s.rawKeep == 0 && !s.exact {
			return "", fmt.Errorf("get_kept: stream %s keeps no raw events%s", s.name, keptInstead(s))
		}
		tbl, what = s.rawTable, "Raw events"
	case "samples":
		if s.samples == "" {
			return "", fmt.Errorf("get_kept: stream %s keeps no samples%s", s.name, keptInstead(s))
		}
		tbl, what = s.samples, fmt.Sprintf("Samples (%d per %s per key)", s.sampleN, durationText(s.samplePer))
	case "anomalies":
		if s.anomalies == "" {
			return "", fmt.Errorf("get_kept: stream %s keeps no unusual events%s", s.name, keptInstead(s))
		}
		d := s.anomaly
		rule := stringOf(d["value"])
		if d["log"] == true {
			rule = "log " + rule
		}
		if d["change"] == true {
			rule += " change"
		}
		tbl, what = s.anomalies, fmt.Sprintf("Unusual events (%s, z > %s)", rule, num(floatOf(d["z"])))
	default:
		return "", fmt.Errorf("get_kept: kind is raw, samples, anomalies or templates")
	}
	cols, err := columnsOf(r, tbl)
	if err != nil {
		return "", err
	}
	filterable := append([]string{}, s.keys...)
	if s.id != "" {
		filterable = append(filterable, s.id)
	}
	fs, err := a.where(filterable, s.intKey)
	if err != nil {
		return "", err
	}
	cond, qargs := sqlFilters(fs, "")
	var conds []string
	if cond != "" {
		conds = append(conds, cond)
	}
	if hasFrom {
		conds = append(conds, "ts >= ?")
		qargs = append(qargs, from)
	}
	if hasTo {
		conds = append(conds, "ts < ?")
		qargs = append(qargs, to)
	}
	if contains != "" {
		if !s.fromLogs {
			return "", fmt.Errorf("get_kept: contains searches log lines, and stream %s does not read logs", s.name)
		}
		conds = append(conds, `line LIKE ? ESCAPE '\'`)
		qargs = append(qargs, likePattern(contains))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	cnt, err := r.Read("SELECT count(*) FROM "+quoteName(tbl)+where, qargs, 0)
	if err != nil {
		return "", fmt.Errorf("get_kept: %v", err)
	}
	total := intAt(first(cnt), 0)
	show := []string{"ts"} // the time first, then the event as it was kept
	for _, col := range cols {
		if col == "res" || col == "w" || col == "slot" || col == "ts" {
			continue
		}
		show = append(show, col)
	}
	orderBy := "ts DESC"
	if !desc {
		orderBy = "ts ASC"
	}
	if unusual {
		orderBy = "abs(z) DESC, ts DESC"
	}
	var sel []string
	for _, col := range show {
		sel = append(sel, quoteName(col))
	}
	rows, err := r.Read(fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s", strings.Join(sel, ", "), quoteName(tbl), where, orderBy), qargs, limit)
	if err != nil {
		return "", fmt.Errorf("get_kept: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s of stream %s", what, s.name)
	var sels []string
	if ft := filterText(fs); ft != "" {
		sels = append(sels, ft)
	}
	if hasFrom {
		sels = append(sels, "from "+when(from))
	}
	if hasTo {
		sels = append(sels, "before "+when(to))
	}
	if contains != "" {
		sels = append(sels, fmt.Sprintf("containing %q", contains))
	}
	if len(sels) > 0 {
		b.WriteString(", " + strings.Join(sels, ", "))
	}
	if total == 0 {
		b.WriteString(": none.\n")
		if kind == "raw" {
			fmt.Fprintf(&b, "Raw events are kept %s; the windows keep the counts (get_windows).\n", rawRule(s))
		}
		return b.String(), nil
	}
	order = "newest first"
	if !desc {
		order = "oldest first"
	}
	if unusual {
		order = "the most unusual first"
	}
	if int64(len(rows.Rows)) < total {
		fmt.Fprintf(&b, ": %d of %d, %s.\n", len(rows.Rows), total, order)
	} else {
		fmt.Fprintf(&b, ": %d, %s.\n", total, order)
	}
	pinned := fixed(fs)
	t := &table{}
	var head []string
	var idx []int
	for i, col := range show {
		if pinned[col] {
			continue
		}
		idx = append(idx, i)
		if col == "ts" {
			head = append(head, "time")
		} else {
			head = append(head, col)
		}
	}
	t.line(head...)
	for _, row := range rows.Rows {
		var f []string
		for _, i := range idx {
			switch show[i] {
			case "ts":
				f = append(f, whenCell(row[i]))
			case "z": // how unusual, to three digits
				if z, ok := asFloat(row[i]); ok {
					f = append(f, digits(z, 3))
				} else {
					f = append(f, cell(row[i]))
				}
			default:
				f = append(f, cell(row[i]))
			}
		}
		t.row(f...)
	}
	b.WriteString(t.String())
	return b.String(), nil
}

func rawRule(s *stream) string {
	if s.exact {
		return fmt.Sprintf("until their %s has been closed %s", s.period, durationText(s.rawAfter))
	}
	return "for " + durationText(s.rawKeep)
}

// keptInstead says what a stream does keep whole, for an error message.
func keptInstead(s *stream) string {
	var k []string
	if s.rawKeep != 0 || s.exact {
		k = append(k, "raw")
	}
	if s.samples != "" {
		k = append(k, "samples")
	}
	if s.anomalies != "" {
		k = append(k, "anomalies")
	}
	if len(k) == 0 {
		return "; it keeps only windows (get_windows)"
	}
	return "; it keeps " + strings.Join(k, " and ")
}

func getTemplates(r Reader, a args, c *catalog, from int64, hasFrom bool, to int64, hasTo bool, contains string, desc bool, limit int) (string, error) {
	if c.templates == "" {
		return "", fmt.Errorf("get_kept: this file reads no logs, so it has no templates")
	}
	if a.has("stream") {
		return "", fmt.Errorf("get_kept: templates belong to the whole file and take no stream")
	}
	fs, err := a.where([]string{"id", "service", "level"}, map[string]bool{"id": true})
	if err != nil {
		return "", err
	}
	cond, qargs := sqlFilters(fs, "")
	var conds []string
	if cond != "" {
		conds = append(conds, cond)
	}
	if hasFrom {
		conds = append(conds, "first_ts >= ?")
		qargs = append(qargs, from)
	}
	if hasTo {
		conds = append(conds, "first_ts < ?")
		qargs = append(qargs, to)
	}
	if contains != "" {
		conds = append(conds, `(template LIKE ? ESCAPE '\' OR example LIKE ? ESCAPE '\')`)
		qargs = append(qargs, likePattern(contains), likePattern(contains))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	cnt, err := r.Read("SELECT count(*) FROM "+quoteName(c.templates)+where, qargs, 0)
	if err != nil {
		return "", err
	}
	total := intAt(first(cnt), 0)
	dir := "DESC"
	if !desc {
		dir = "ASC"
	}
	rows, err := r.Read(fmt.Sprintf("SELECT id, service, level, first_ts, last_ts, n, template, example FROM %s%s ORDER BY first_ts %s, id %s",
		quoteName(c.templates), where, dir, dir), qargs, limit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Log templates")
	var sels []string
	if ft := filterText(fs); ft != "" {
		sels = append(sels, ft)
	}
	if hasFrom {
		sels = append(sels, "first seen from "+when(from))
	}
	if hasTo {
		sels = append(sels, "first seen before "+when(to))
	}
	if contains != "" {
		sels = append(sels, fmt.Sprintf("containing %q", contains))
	}
	if len(sels) > 0 {
		b.WriteString(", " + strings.Join(sels, ", "))
	}
	if total == 0 {
		b.WriteString(": none.\n")
		return b.String(), nil
	}
	ord := "newest first"
	if !desc {
		ord = "oldest first"
	}
	if int64(len(rows.Rows)) < total {
		fmt.Fprintf(&b, ": %d of %d, %s.", len(rows.Rows), total, ord)
	} else {
		fmt.Fprintf(&b, ": %d, %s.", total, ord)
	}
	b.WriteString(" <*> marks the parts that vary; lines counts the lines that matched so far, and example is one of them.\n")
	t := &table{}
	t.line("id", "service", "level", "first_seen", "last_seen", "lines", "template", "example")
	for _, row := range rows.Rows {
		t.row(cell(row[0]), cell(row[1]), cell(row[2]), whenCell(row[3]), whenCell(row[4]), cell(row[5]), cell(row[6]), cell(row[7]))
	}
	b.WriteString(t.String())
	return b.String(), nil
}
