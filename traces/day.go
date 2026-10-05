package traces

import (
	"bufio"
	"fmt"
	"io"
	"sort"
)

// Day is agent runs laid over time, with every model call in the order its reply came back.
type Day struct {
	Runs  []*Run
	Calls []Call
	byID  map[string]*Run
}

// Call is one model call of a run.
type Call struct {
	Run *Run
	Seq int   // the call's number in its run, from 1
	TS  int64 // when the reply came back
}

// ReadDay reads runs, one JSON line each (as tools/traces-prepare.mjs writes them), and orders
// their calls by the time their replies came back.
func ReadDay(r io.Reader) (*Day, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	d := &Day{byID: map[string]*Run{}}
	line := 0
	for {
		b, err := br.ReadBytes('\n')
		if len(b) > 0 && !(len(b) == 1 && b[0] == '\n') {
			line++
			run, perr := ParseRun(b)
			if perr != nil {
				return nil, fmt.Errorf("line %d: %v", line, perr)
			}
			if d.byID[run.ID] != nil {
				return nil, fmt.Errorf("line %d: run %s came twice", line, run.ID)
			}
			d.add(run)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	d.order()
	return d, nil
}

func (d *Day) add(r *Run) {
	d.Runs = append(d.Runs, r)
	d.byID[r.ID] = r
	for seq, i := range r.Replies() {
		d.Calls = append(d.Calls, Call{Run: r, Seq: seq + 1, TS: r.Start + r.Messages[i].At})
	}
}

// order sorts the calls by time, then run and number, which keeps each run's calls in order.
func (d *Day) order() {
	sort.SliceStable(d.Calls, func(a, b int) bool {
		x, y := d.Calls[a], d.Calls[b]
		if x.TS != y.TS {
			return x.TS < y.TS
		}
		if x.Run.ID != y.Run.ID {
			return x.Run.ID < y.Run.ID
		}
		return x.Seq < y.Seq
	})
}

// Run finds a run by its id.
func (d *Day) Run(id string) *Run { return d.byID[id] }

// Replay hands a day's calls to a store in the order their replies came back, as a tracer beside
// the agents would.
type Replay struct {
	Day   *Day
	Store *Store
	Next  int // the calls handed over so far
}

// Until hands over every call whose reply came back by t and says how many it handed over.
func (p *Replay) Until(t int64) (int, error) {
	n := 0
	for p.Next < len(p.Day.Calls) && p.Day.Calls[p.Next].TS <= t {
		c := p.Day.Calls[p.Next]
		if err := p.Store.Put(c.Run, c.Seq); err != nil {
			return n, err
		}
		p.Next++
		n++
	}
	return n, nil
}

// Done says whether every call has been handed over.
func (p *Replay) Done() bool { return p.Next >= len(p.Day.Calls) }

// Again reports the calls of a run that are already stored once more, as a tracer that retries an
// upload would. The store knows them and the exact streams refuse them as repeats, so nothing is
// counted twice. It says how many calls it reported.
func (s *Store) Again(r *Run) (int, error) {
	n := 0
	for seq := 1; seq <= len(r.Replies()); seq++ {
		if !s.known[r.CallID(seq)] {
			break
		}
		if err := s.Put(r, seq); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
