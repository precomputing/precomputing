//go:build cgo

// Command filecompare compares two Precomputing files table by table, value by value, to the
// last bit, and exits with status 1 if they differ. It is how the native Engine's files are
// checked against the files the demos write in the browser. -skip leaves out tables that hold
// input rather than results, such as Demo 3's price list and quota limits.
//
//	go run ./tools/filecompare [-skip t1,t2] engine.db browser.db
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"precomputing.com/precomputing/internal/filecmp"
	"precomputing.com/precomputing/internal/sqlite"
)

func main() {
	skip := flag.String("skip", "", "tables to leave out, separated by commas")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: filecompare [-skip t1,t2] A.db B.db")
		os.Exit(2)
	}
	for _, t := range strings.Split(*skip, ",") {
		if t = strings.TrimSpace(t); t != "" {
			filecmp.Skip[t] = true
		}
	}
	a, err := sqlite.OpenReadOnly(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer a.Close()
	b, err := sqlite.OpenReadOnly(flag.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer b.Close()
	tables, err := filecmp.Compare(a, b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	rows, values, diffs := 0, 0, 0
	for _, t := range tables {
		rows += t.Rows
		values += t.Values
		diffs += t.Diffs
		line := fmt.Sprintf("%-40s %9d rows %11d values", t.Name, t.Rows, t.Values)
		if t.Diffs > 0 {
			line += fmt.Sprintf("  %d differ: %s", t.Diffs, t.First)
		}
		fmt.Println(line)
	}
	fmt.Printf("%d tables, %d rows, %d values, %d differences\n", len(tables), rows, values, diffs)
	if diffs > 0 {
		os.Exit(1)
	}
}
