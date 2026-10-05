package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/mcp"
)

// mcpCmd serves one file to an AI agent over stdio, the way desktop agents start local tools:
// JSON-RPC messages one per line on standard input, answers on standard output.
func mcpCmd(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "precomputing mcp: give exactly one file")
		return 2
	}
	path := fs.Arg(0)
	if _, err := os.Stat(path); err != nil {
		return fail("%v", err)
	}
	db, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	srv := &mcp.Server{Reader: &mcp.SQLiteReader{DB: db}, Name: "precomputing", Title: "Precomputing: " + filepath.Base(path), Version: version.Version}
	if err := srv.ServeStdio(os.Stdin, os.Stdout); err != nil {
		return fail("%v", err)
	}
	return 0
}
