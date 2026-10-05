//go:build cgo

// Package filecmp compares two Precomputing files table by table, value by value, to the last bit.
package filecmp

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"precomputing.com/precomputing/internal/sqlite"
)

// Table is the result of comparing one table.
type Table struct {
	Name   string
	Rows   int // rows in the first file
	Values int // values compared
	Diffs  int // rows missing or extra, plus values that differ
	First  string
}

// Skip lists tables left out of comparisons: the Engine's record of its senders, which a file
// written by the triggers alone does not have.
var Skip = map[string]bool{"_precomputing_sources": true}

// Compare reads every table of a and b. Tables without a rowid are ordered by all their columns;
// rowid tables (raw events, anomalies) by rowid, and their rowid values are not compared, since
// the Engine does not write raw events that the next distill would delete.
func Compare(a, b *sqlite.DB) ([]Table, error) {
	ta, err := tables(a)
	if err != nil {
		return nil, err
	}
	tb, err := tables(b)
	if err != nil {
		return nil, err
	}
	var names []string
	for n := range ta {
		names = append(names, n)
	}
	for n := range tb {
		if _, ok := ta[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []Table
	for _, name := range names {
		noRowid, inA := ta[name]
		_, inB := tb[name]
		if !inA || !inB {
			out = append(out, Table{Name: name, Diffs: 1, First: "only in one file"})
			continue
		}
		ra, err := dump(a, name, noRowid)
		if err != nil {
			return nil, err
		}
		rb, err := dump(b, name, noRowid)
		if err != nil {
			return nil, err
		}
		t := Table{Name: name, Rows: len(ra)}
		if len(ra) != len(rb) {
			t.Diffs = abs(len(ra) - len(rb))
			t.First = fmt.Sprintf("%d rows against %d", len(ra), len(rb))
		} else {
			for i := range ra {
				for j := range ra[i] {
					t.Values++
					if !same(ra[i][j], rb[i][j]) {
						if t.Diffs == 0 {
							t.First = fmt.Sprintf("row %d column %d: %v against %v", i, j, ra[i][j], rb[i][j])
						}
						t.Diffs++
					}
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// Identical reports whether every table matched.
func Identical(ts []Table) bool {
	for _, t := range ts {
		if t.Diffs != 0 {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func same(a, b any) bool {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return math.Float64bits(fa) == math.Float64bits(fb)
	}
	return a == b
}

func tables(db *sqlite.DB) (map[string]bool, error) {
	st, err := db.PrepareOnce("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	out := map[string]bool{}
	for {
		ok, err := st.Step()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		if !Skip[st.Text(0)] {
			out[st.Text(0)] = strings.Contains(st.Text(1), "WITHOUT ROWID")
		}
	}
}

func dump(db *sqlite.DB, table string, noRowid bool) ([][]any, error) {
	st, err := db.PrepareOnce("SELECT * FROM " + table)
	if err != nil {
		return nil, err
	}
	ncol := st.Columns()
	st.Finalize()
	order := "rowid"
	if noRowid {
		var cols []string
		for i := 1; i <= ncol; i++ {
			cols = append(cols, fmt.Sprint(i))
		}
		order = strings.Join(cols, ", ")
	}
	st, err = db.PrepareOnce("SELECT * FROM " + table + " ORDER BY " + order)
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	var rows [][]any
	for {
		ok, err := st.Step()
		if err != nil {
			return nil, err
		}
		if !ok {
			return rows, nil
		}
		row := make([]any, ncol)
		for i := range row {
			row[i] = st.Value(i)
		}
		rows = append(rows, row)
	}
}
