package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/traces"
)

// tracesCmd keeps the model calls of AI agent runs in a file: each message and tool list once, by
// its SHA-256, each call as the list of its pieces, secrets masked, and every call metered by the
// policy's exact streams (examples/traces.precompute). With --rebuild it prints one call back.
func tracesCmd(args []string) int {
	fs := flag.NewFlagSet("traces", flag.ContinueOnError)
	policyPath := fs.String("policy", "", "the policy, to make a new file")
	again := fs.String("again", "", "runs to report a second time after the others, separated by commas")
	every := fs.Int64("every", 60, "seconds of the runs' time between checkpoints")
	syncMode := fs.String("sync", "normal", "full, normal or off")
	rebuild := fs.String("rebuild", "", "print the request of this call, rebuilt from the file")
	reply := fs.Bool("reply", false, "with --rebuild, print the reply instead")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "precomputing traces: give exactly one file")
		return 2
	}
	path := fs.Arg(0)
	if *rebuild != "" {
		if _, err := os.Stat(path); err != nil {
			return fail("%v", err)
		}
		st, err := sqlitestore.Open(path, sqlitestore.Options{})
		if err != nil {
			return fail("%v", err)
		}
		defer st.Close()
		req, rep, err := traces.Rebuild(st, *rebuild)
		if err != nil {
			return fail("%v", err)
		}
		if *reply {
			req = rep
		}
		fmt.Println(req)
		return 0
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<16)
	var r io.Reader = in
	if magic, _ := in.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fail("reading runs: %v", err)
		}
		r = gz
	}
	day, err := traces.ReadDay(r)
	if err != nil {
		return fail("reading runs: %v", err)
	}
	eng, st, err := openEngine(path, *policyPath, *syncMode)
	if err != nil {
		return fail("%v", err)
	}
	defer st.Close()
	store, err := traces.Open(eng)
	if err != nil {
		return fail("%v", err)
	}
	rp := &traces.Replay{Day: day, Store: store}
	for !rp.Done() {
		if _, err := rp.Until(day.Calls[rp.Next].TS + *every); err != nil {
			return fail("%v", err)
		}
		if err := eng.Checkpoint(); err != nil {
			return fail("checkpoint: %v", err)
		}
	}
	for _, id := range strings.Split(*again, ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		r := day.Run(id)
		if r == nil {
			return fail("--again: no run %s", id)
		}
		if _, err := store.Again(r); err != nil {
			return fail("%v", err)
		}
	}
	if err := eng.Checkpoint(); err != nil {
		return fail("checkpoint: %v", err)
	}
	s := store.Stats()
	var cost float64
	st.Query("SELECT coalesce(sum(value), 0) FROM repo_cost_day", nil, func(row []any) error {
		cost, _ = row[0].(float64)
		return nil
	})
	fmt.Printf("%d runs, %d calls kept, %d reported again and refused, %d secrets masked\n", len(day.Runs), s.Calls, s.Repeats, s.Masked)
	fmt.Printf("requests as sent %s; kept as %d new pieces, %s\n", sizeOf(s.RequestBytes), s.Pieces, sizeOf(s.PieceBytes))
	fmt.Printf("cost in the file $%.2f, at the policy's prices\n", cost/1e9)
	return 0
}

func sizeOf(n int64) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d bytes", n)
}
