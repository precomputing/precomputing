package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"precomputing.com/precomputing/internal/sqlite"
)

// result is the rows of one read.
type result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// query runs one read-only statement.
func query(db *sqlite.DB, sql string, args ...any) (*result, error) {
	st, err := db.PrepareOnce(sql)
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	if !st.ReadOnly() {
		return nil, fmt.Errorf("only reading is allowed here; the file changes through its streams")
	}
	for i, a := range args {
		if err := st.Bind(i+1, a); err != nil {
			return nil, err
		}
	}
	r := &result{}
	for i := 0; i < st.Columns(); i++ {
		r.Columns = append(r.Columns, st.ColumnName(i))
	}
	for {
		ok, err := st.Step()
		if err != nil {
			return nil, err
		}
		if !ok {
			return r, nil
		}
		row := make([]any, len(r.Columns))
		for i := range row {
			row[i] = st.Value(i)
		}
		r.Rows = append(r.Rows, row)
	}
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case float64:
		return fmt.Sprint(x)
	}
	return fmt.Sprint(v)
}

// print writes rows as an aligned table, numbers to the right.
func (r *result) print() {
	width := make([]int, len(r.Columns))
	for i, c := range r.Columns {
		width[i] = len(c)
	}
	text := make([][]string, len(r.Rows))
	for j, row := range r.Rows {
		text[j] = make([]string, len(row))
		for i, v := range row {
			s := cell(v)
			if len(s) > 60 {
				s = s[:57] + "..."
			}
			s = strings.ReplaceAll(s, "\n", " ")
			text[j][i] = s
			if len(s) > width[i] {
				width[i] = len(s)
			}
		}
	}
	line := func(cells []string, row []any) {
		var b strings.Builder
		for i, c := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			num := false
			if row != nil {
				switch row[i].(type) {
				case int64, float64:
					num = true
				}
			}
			if num {
				fmt.Fprintf(&b, "%*s", width[i], c)
			} else {
				fmt.Fprintf(&b, "%-*s", width[i], c)
			}
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
	line(r.Columns, nil)
	var rule []string
	for _, w := range width {
		rule = append(rule, strings.Repeat("-", w))
	}
	line(rule, nil)
	for j := range text {
		line(text[j], r.Rows[j])
	}
}

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func getCmd(args []string) int {
	asJSON := false
	var rest []string
	for _, a := range args {
		if a == "--json" || a == "-json" {
			asJSON = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "precomputing get: give a file and a name or a query")
		return 2
	}
	db, err := sqlite.OpenReadOnly(rest[0])
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	sql := rest[1]
	if nameRE.MatchString(sql) {
		sql = "SELECT * FROM " + sql
	}
	r, err := query(db, sql)
	if err != nil {
		return fail("%v", err)
	}
	if asJSON {
		out := make([]map[string]any, len(r.Rows))
		for j, row := range r.Rows {
			out[j] = map[string]any{}
			for i, c := range r.Columns {
				out[j][c] = row[i]
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return 0
	}
	r.print()
	return 0
}

func statsCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "precomputing stats: give one file")
		return 2
	}
	db, err := sqlite.OpenReadOnly(args[0])
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	fi, err := os.Stat(args[0])
	if err != nil {
		return fail("%v", err)
	}
	r, err := query(db, `SELECT name, sum(pgsize) AS bytes FROM dbstat GROUP BY name ORDER BY bytes DESC`)
	if err != nil {
		return fail("%v", err)
	}
	t := &result{Columns: []string{"table or index", "rows", "bytes"}}
	for _, row := range r.Rows {
		name := row[0].(string)
		var rows any = ""
		if n, err := query(db, fmt.Sprintf(`SELECT count(*) FROM "%s"`, strings.ReplaceAll(name, `"`, `""`))); err == nil && len(n.Rows) == 1 {
			isTable, _ := query(db, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", name)
			if isTable != nil && len(isTable.Rows) == 1 {
				rows = n.Rows[0][0]
			}
		}
		t.Rows = append(t.Rows, []any{name, rows, row[1]})
	}
	fmt.Printf("%s: %d bytes on disk\n\n", args[0], fi.Size())
	t.print()
	if src, err := query(db, `SELECT source, seq AS last_seq, events, datetime(newest, 'unixepoch') AS newest_utc FROM _precomputing_sources ORDER BY source`); err == nil && len(src.Rows) > 0 {
		fmt.Println()
		src.print()
	}
	return 0
}

func inspectCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "precomputing inspect: give one file")
		return 2
	}
	db, err := sqlite.OpenReadOnly(args[0])
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	meta, err := query(db, "SELECT key, value FROM _precomputing")
	if err != nil {
		return fail("%s is not a Precomputing file: %v", args[0], err)
	}
	m := map[string]string{}
	for _, row := range meta.Rows {
		m[row[0].(string)], _ = row[1].(string)
	}
	fmt.Printf("File format %s, made by %s.\n\nPolicy:\n\n", m["format"], m["compiler"])
	for _, l := range strings.Split(strings.TrimRight(m["policy"], "\n"), "\n") {
		fmt.Println("  " + l)
	}
	objs, err := query(db, "SELECT name, kind, stream, detail FROM _precomputing_objects ORDER BY rowid")
	if err == nil {
		fmt.Println("\nWhat it holds:")
		fmt.Println()
		objs.print()
	}
	return 0
}
