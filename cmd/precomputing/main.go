// Command precomputing compiles policies into SQLite and runs them in the Engine.
package main

import (
	"fmt"
	"os"

	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/internal/version"
)

const usage = `Precomputing %s: answers kept ready as data arrives.

Usage:
  precomputing compile [--distill] [-o FILE] POLICY.precompute
      Print the SQL that sets up a SQLite file for the policy. --distill prints the
      statements that let old detail fade; run them every few minutes with :now bound
      to the newest event time.

  precomputing put [--policy POLICY] [--seq] [--format csv|json] [--stream NAME]
                   [--source NAME] [--every 100ms] [--sync full|normal|off] FILE.db
      Keep FILE.db current with the Engine, reading events from standard input.
      CSV lines are [seq,] ts, keys..., values..., in the policy's order; JSON lines
      name their fields. It prints "ready SEQ" when it starts and "ok SEQ" after each
      checkpoint: every event up to SEQ is then in the file. After a crash, send again
      from SEQ + 1; anything already in the file is skipped.

  precomputing put --lines [--policy POLICY] [--source NAME] FILE.db < app.log
      Read log lines with the policy's logs block: learn each line's template and feed
      the streams from logs. A line's sequence number is its line number, so after a
      crash the same file can be sent again and the lines already kept are skipped.

  precomputing import DASHBOARD.json
      Print a policy that keeps a dashboard's panels ready from log lines.
  precomputing templates FILE.db
      The log templates a file has learned, with how often each was seen.

  precomputing serve [--policy POLICY] [--addr localhost:8080] [--every 100ms] [--sync full]
                     [--read-only] [--token-file FILE] [--allow-origin ORIGINS] FILE.db
      The same over HTTP. POST /v1/events (JSON or CSV lines) answers once the
      events are in the file; GET /v1/answers/NAME, /v1/query?sql=..., /v1/stats read.
      POST /mcp serves AI agents over the Model Context Protocol. --read-only serves a
      file without taking events. With --token-file every request needs a token.

  precomputing mcp FILE.db
      Serve a file to an AI agent over stdio, for agents that start local tools.

  precomputing traces [--policy POLICY] [--again RUNS] FILE.db < runs.jsonl
      Keep the model calls of AI agent runs: every message and tool list once, by
      its SHA-256, secrets masked, and every call metered by the policy's streams
      (examples/traces.precompute). Runs are JSON lines, gzipped or not, as
      tools/traces-prepare.mjs writes them.
  precomputing traces --rebuild CALL [--reply] FILE.db
      Print a call's request, rebuilt from the file byte for byte, or its reply.

  precomputing get FILE.db NAME|SQL [--json]
      Print a precompute, a table, or the result of a read-only query.
  precomputing stats FILE.db
      Rows and bytes per table, and where each sender is.
  precomputing inspect FILE.db
      The policy a file was made from and what it holds.

  precomputing demo [--every-trades 10000] [--race 200000] [--db FILE] [--json]
      Run Demo 2's simulated trading day through the Engine on this machine, check
      every candle against a recount, and race the compiled triggers.

  precomputing version
  precomputing help

Example:
  precomputing compile latency.precompute | sqlite3 latency.db
  sqlite3 latency.db "INSERT INTO latency (ts, endpoint, ms) VALUES (1790586000, '/api/search', 31.5)"
  sqlite3 latency.db "SELECT * FROM p99_ms"
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version.Version)
		os.Exit(2)
	}
	args := os.Args[2:]
	var code int
	switch os.Args[1] {
	case "compile":
		code = compileCmd(args, os.Stdout, os.Stderr)
	case "put":
		code = putCmd(args)
	case "serve":
		code = serveCmd(args)
	case "mcp":
		code = mcpCmd(args)
	case "traces":
		code = tracesCmd(args)
	case "get":
		code = getCmd(args)
	case "stats":
		code = statsCmd(args)
	case "inspect":
		code = inspectCmd(args)
	case "import":
		code = importCmd(args)
	case "templates":
		code = templatesCmd(args)
	case "demo":
		code = demoCmd(args)
	case "version", "--version", "-v":
		fmt.Printf("precomputing %s (file format %d, SQLite %s)\n", version.Version, version.Format, sqlite.Version())
	case "help", "--help", "-h":
		fmt.Printf(usage, version.Version)
	default:
		fmt.Fprintf(os.Stderr, "precomputing: unknown command %q\n\n", os.Args[1])
		fmt.Fprintf(os.Stderr, usage, version.Version)
		code = 2
	}
	os.Exit(code)
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "precomputing: "+format+"\n", a...)
	return 1
}
