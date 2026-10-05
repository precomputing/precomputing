package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/logs"
)

// putCmd keeps a file current from events on standard input, checkpointing every so often and
// acknowledging on standard output what the file holds.
func putCmd(args []string) int {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	pol := fs.String("policy", "", "policy to make a new file from")
	seq := fs.Bool("seq", false, "CSV lines start with a sequence number")
	format := fs.String("format", "csv", "csv or json")
	stream := fs.String("stream", "", "the stream CSV lines belong to (default: the policy's only stream)")
	source := fs.String("source", "stdin", "name of this sender, for its sequence numbers")
	every := fs.Duration("every", 100*time.Millisecond, "checkpoint at least this often")
	sync := fs.String("sync", "full", "full, normal or off: how hard each checkpoint waits for the disk")
	trace := fs.Bool("trace", false, `print "writing SEQ" as each checkpoint starts`)
	logLines := fs.Bool("lines", false, "the input is log lines, read with the policy's logs block; a line's sequence number is its line number")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "precomputing put: give exactly one file")
		return 2
	}
	eng, st, err := openEngine(fs.Arg(0), *pol, *sync)
	if err != nil {
		return fail("%v", err)
	}
	defer st.Close()
	var p *parser
	var red *logs.Reducer
	if *logLines {
		if red, err = logs.New(eng); err != nil {
			return fail("%v", err)
		}
	} else if p, err = newParser(eng, *format, *stream, *seq); err != nil {
		return fail("%v", err)
	}
	out := bufio.NewWriter(os.Stdout)
	say := func(format string, a ...any) {
		fmt.Fprintf(out, format+"\n", a...)
		out.Flush()
	}
	say("ready %d", eng.Committed(*source))

	lines := make(chan []byte, 1<<14)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(os.Stdin, 1<<20)
		for {
			b, err := r.ReadBytes('\n')
			if len(b) > 0 {
				lines <- b
			}
			if err != nil {
				close(lines)
				if !errors.Is(err, io.EOF) {
					readErr <- err
				}
				return
			}
		}
	}()

	var n, bad int64
	var dirty bool
	checkpoint := func() error {
		if !dirty {
			return nil
		}
		if *trace {
			say("writing %d", eng.Applied(*source))
		}
		if err := eng.Checkpoint(); err != nil {
			return err
		}
		dirty = false
		say("ok %d", eng.Committed(*source))
		return nil
	}
	tick := time.NewTicker(*every)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case b, ok := <-lines:
			if !ok {
				if err := checkpoint(); err != nil {
					return fail("checkpoint: %v", err)
				}
				s := eng.Stats()
				if red != nil {
					fmt.Fprintf(os.Stderr, "precomputing: %d log lines, %d events applied, %d lines already in the file, %d not read, %d templates, %d checkpoints\n",
						n, s.Events, s.Duplicates, red.Stats().NotRead, len(red.Templates()), s.Checkpoints)
				} else {
					fmt.Fprintf(os.Stderr, "precomputing: %d lines, %d events applied, %d skipped as already applied, %d refused by the stream, %d not read, %d checkpoints\n",
						n, s.Events-s.Refused, s.Duplicates, s.Refused, bad, s.Checkpoints)
				}
				select {
				case err := <-readErr:
					return fail("reading: %v", err)
				default:
				}
				return 0
			}
			n++
			if red != nil {
				if _, err := red.Put(*source, n, string(b)); err != nil {
					var stopped *engine.StoppedError
					if errors.As(err, &stopped) {
						return fail("line %d: %v", n, err)
					}
					fmt.Fprintf(os.Stderr, "precomputing: line %d: %v\n", n, err)
				}
				dirty = true
				if time.Since(last) >= *every {
					if err := checkpoint(); err != nil {
						return fail("checkpoint: %v", err)
					}
					last = time.Now()
				}
				continue
			}
			if len(b) == 0 || b[0] == '\n' || b[0] == '#' {
				continue
			}
			ev, err := p.parse(b)
			if err == nil {
				err = eng.PutID(ev.stream, *source, ev.seq, ev.ts, ev.id, ev.key, ev.vals)
			}
			if err != nil {
				var stopped *engine.StoppedError
				if errors.As(err, &stopped) {
					return fail("line %d: %v", n, err)
				}
				bad++
				fmt.Fprintf(os.Stderr, "precomputing: line %d refused: %v\n", n, err)
				continue
			}
			dirty = true
			if time.Since(last) >= *every {
				if err := checkpoint(); err != nil {
					return fail("checkpoint: %v", err)
				}
				last = time.Now()
			}
		case <-tick.C:
			if time.Since(last) >= *every {
				if err := checkpoint(); err != nil {
					return fail("checkpoint: %v", err)
				}
				last = time.Now()
			}
		}
	}
}
