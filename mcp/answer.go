package mcp

import (
	"fmt"
	"strings"
)

// getAnswer reads a precompute, a quota, a refusal count or one of the file's other views.
func getAnswer(r Reader, a args) (string, error) {
	name, err := a.str("name", true)
	if err != nil {
		return "", err
	}
	limit, err := a.integer("limit", 50, 1, 500)
	if err != nil {
		return "", err
	}
	period, err := a.str("period", false)
	if err != nil {
		return "", err
	}
	c, err := loadCatalog(r)
	if err != nil {
		return "", err
	}
	headline := ""
	var order []string
	an := c.answerBy[name]
	switch {
	case an != nil:
		headline = an.describe()
		if an.accuracy > 0 {
			headline += fmt.Sprintf(", within %s%% of the exact value", num(an.accuracy*100))
		}
		order = append(append(order, an.by...), "period")
	default:
		ok := false
		for _, t := range c.refusals {
			if t == name {
				ok, headline = true, name+": events refused, by reason and key"
				order = []string{"reason"}
			}
		}
		for _, o := range c.others {
			if o.name == name {
				ok, headline = true, fmt.Sprintf("%s (%s)", name, o.kind)
			}
		}
		if !ok {
			var names []string
			for _, x := range c.answers {
				names = append(names, x.name)
			}
			names = append(names, c.refusals...)
			for _, o := range c.others {
				names = append(names, o.name)
			}
			if len(names) == 0 {
				return "", fmt.Errorf("get_answer: this file keeps no precomputes or views; get_windows and get_kept read its streams")
			}
			return "", fmt.Errorf("get_answer: the file has no answer %q; it has %s", name, strings.Join(names, ", "))
		}
	}
	cols, err := columnsOf(r, name)
	if err != nil {
		return "", err
	}
	intCol := map[string]bool{}
	if an != nil {
		if s := c.byName[an.stream]; s != nil {
			intCol = s.intKey
		}
	}
	fs, err := a.where(cols, intCol)
	if err != nil {
		return "", err
	}
	cond, qargs := sqlFilters(fs, "")
	var conds []string
	if cond != "" {
		conds = append(conds, cond)
	}
	hasPeriod := false
	for _, col := range cols {
		if col == "period" {
			hasPeriod = true
		}
	}
	if period != "" {
		if !hasPeriod {
			return "", fmt.Errorf("get_answer: %s is not kept per period, so it takes no period", name)
		}
		conds = append(conds, "(period = ? OR substr(period, 1, ?) = ? AND substr(period, ?, 1) IN ('-', 'T'))")
		qargs = append(qargs, period, len(period), period, len(period)+1)
	}
	sql := "SELECT * FROM " + quoteName(name)
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	var ob []string
	for _, o := range order {
		for _, col := range cols {
			if col == o {
				ob = append(ob, quoteName(o))
			}
		}
	}
	if len(ob) > 0 {
		sql += " ORDER BY " + strings.Join(ob, ", ")
	}
	rows, err := r.Read(sql, qargs, limit)
	if err != nil {
		return "", fmt.Errorf("get_answer: %v", err)
	}
	var b strings.Builder
	b.WriteString(headline)
	var sel []string
	if f := filterText(fs); f != "" {
		sel = append(sel, f)
	}
	if period != "" {
		sel = append(sel, "period "+period)
	}
	if len(sel) > 0 {
		b.WriteString(". Rows with " + strings.Join(sel, "; "))
	}
	b.WriteString(".\n")
	if len(rows.Rows) == 0 {
		b.WriteString("No rows match.\n")
		return b.String(), nil
	}
	pinned := fixed(fs)
	var keep []int
	for i, col := range rows.Columns {
		if !pinned[col] && !(col == "period" && period != "" && samePeriod(rows, i)) {
			keep = append(keep, i)
		}
	}
	t := &table{}
	var head []string
	for _, i := range keep {
		head = append(head, rows.Columns[i])
	}
	t.line(head...)
	for _, row := range rows.Rows {
		var f []string
		for _, i := range keep {
			f = append(f, cell(row[i]))
		}
		t.row(f...)
	}
	b.WriteString(t.String())
	if rows.More {
		fmt.Fprintf(&b, "(first %d rows; narrow with where or period, or raise limit)\n", len(rows.Rows))
	}
	return b.String(), nil
}

// samePeriod reports whether every row has the same period, so the column says nothing new.
func samePeriod(rows *Rows, i int) bool {
	for _, row := range rows.Rows {
		if cell(row[i]) != cell(rows.Rows[0][i]) {
			return false
		}
	}
	return true
}

// runQuery runs one read-only statement.
func runQuery(r Reader, a args) (string, error) {
	sql, err := a.str("sql", true)
	if err != nil {
		return "", err
	}
	limit, err := a.integer("limit", 100, 1, 1000)
	if err != nil {
		return "", err
	}
	rows, err := r.Read(sql, nil, limit)
	if err != nil {
		return "", fmt.Errorf("query: %v", err)
	}
	t := &table{}
	t.line(rows.Columns...)
	for _, row := range rows.Rows {
		f := make([]string, len(row))
		for i, v := range row {
			f[i] = cell(v)
		}
		t.row(f...)
	}
	out := t.String()
	if rows.More {
		out += fmt.Sprintf("(first %d rows; raise limit or narrow the query)\n", len(rows.Rows))
	} else if len(rows.Rows) == 0 {
		out += "(no rows)\n"
	}
	return out, nil
}
