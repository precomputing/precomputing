package main

import (
	"fmt"
	"os"
	"strings"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/logs"
)

// importCmd prints the policy that keeps a dashboard's panels ready.
func importCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "precomputing import: give one dashboard file")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fail("%v", err)
	}
	imp, err := logs.Import(data)
	if err != nil {
		return fail("%s: %v", args[0], err)
	}
	fmt.Print(imp.Policy)
	return 0
}

// templatesCmd lists the log templates a file holds.
func templatesCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "precomputing templates: give one file")
		return 2
	}
	if _, err := os.Stat(args[0]); err != nil {
		return fail("%v", err)
	}
	st, err := sqlitestore.Open(args[0], sqlitestore.Options{})
	if err != nil {
		return fail("%v", err)
	}
	defer st.Close()
	var rows [][]string
	err = st.Query("SELECT id, service, level, n, template FROM "+compile.TemplatesTable+" ORDER BY n DESC, id", nil, func(r []any) error {
		rows = append(rows, []string{fmt.Sprint(r[0]), fmt.Sprint(r[1]), fmt.Sprint(r[2]), fmt.Sprint(r[3]), fmt.Sprint(r[4])})
		return nil
	})
	if err != nil {
		return fail("%s holds no log templates: %v", args[0], err)
	}
	fmt.Printf("%5s  %-12s %-6s %10s  %s\n", "id", "service", "level", "lines", "template")
	for _, r := range rows {
		fmt.Printf("%5s  %-12s %-6s %10s  %s\n", r[0], r[1], r[2], r[3], strings.TrimSpace(r[4]))
	}
	return 0
}
