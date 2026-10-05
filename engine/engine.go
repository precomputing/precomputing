// Package engine runs a policy in memory, at native speed, and keeps the same SQLite file the
// compiled SQL keeps. Events update window summaries, sketches, samples, baselines and
// precomputed answers held in memory; a checkpoint writes what changed to the file in one
// transaction, with the sequence number of the last event it covers, and then lets old detail
// fade with the policy's distill statements. After a crash the Engine carries on from the file,
// and a sender that resends from the last acknowledged sequence number loses nothing and counts
// nothing twice.
//
// Every update follows the compiled trigger step by step, with SQLite's arithmetic, so a file
// the Engine writes holds the same rows, bit for bit, as a file the triggers write from the same
// events. Either runtime can open the other's file and carry on.
package engine

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/policy"
)

// SourcesTable records, for each sender, the last sequence number the file holds.
// It is the one table the Engine adds to the compiled schema.
const SourcesTable = "_precomputing_sources"

const sourcesSchema = `CREATE TABLE IF NOT EXISTS _precomputing_sources (
  source TEXT PRIMARY KEY,  -- who sends the events
  seq    INTEGER NOT NULL,  -- the last sequence number the file holds from it
  events INTEGER NOT NULL,  -- events the file holds from it
  newest INTEGER NOT NULL   -- the newest event time from it
)`

// Engine keeps a policy's answers ready in memory and checkpoints them to its file.
// It is not safe for concurrent use; callers serialize access.
type Engine struct {
	pol     *policy.Policy
	layout  *compile.Layout
	store   Store
	log     func(float64) float64
	streams []*stream
	byName  map[string]*stream
	sources map[string]*source
	now     int64
	broken  *StoppedError
	stats   Stats
	states  []State
}

type source struct {
	name      string
	committed int64 // last sequence number in the file
	applied   int64 // last sequence number applied in memory
	events    int64 // events in the file
	pending   int64 // events applied since the last checkpoint
	newest    int64
	changed   bool
}

// Stats counts what an Engine has done since it opened.
type Stats struct {
	Events      int64 // events applied
	Duplicates  int64 // events skipped because their sequence number was already applied
	Refused     int64 // events an exact stream refused: repeats, late ones, ones for closed periods
	Checkpoints int64
	RowsWritten int64
	Reads       int64 // times the file was read for a window, baseline or precompute row
}

// Open starts an Engine on a store. When the file is new, pol must be given and its schema is
// created. When the file already holds a policy, pol may be nil (the file's own policy is used) or
// must compile to the same schema.
func Open(store Store, pol *policy.Policy, name string) (*Engine, error) {
	var filePolicy string
	exists := false
	if err := store.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = '_precomputing'", nil, func([]any) error {
		exists = true
		return nil
	}); err != nil {
		return nil, err
	}
	if exists {
		format := ""
		err := store.Query("SELECT key, value FROM _precomputing WHERE key IN ('format', 'policy')", nil, func(row []any) error {
			v, _ := row[1].(string)
			if row[0] == "format" {
				format = v
			} else {
				filePolicy = v
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if format != fmt.Sprint(version.Format) {
			return nil, fmt.Errorf("the file has format %q; this Engine reads format %d", format, version.Format)
		}
		fp, err := policy.Parse(filePolicy)
		if err != nil {
			return nil, fmt.Errorf("the file's own policy does not read: %v", err)
		}
		if pol == nil {
			pol = fp
		} else if !sameSchema(pol, fp) {
			return nil, errors.New("the file was made from a different policy; open it without a policy to use its own")
		}
	} else if pol == nil {
		return nil, errors.New("a new file needs a policy")
	}
	layout, err := compile.NewLayout(pol, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := store.Exec(layout.Output.Schema); err != nil {
			return nil, err
		}
	}
	if err := store.Exec(sourcesSchema); err != nil {
		return nil, err
	}
	e := &Engine{pol: pol, layout: layout, store: store, log: store.Log, byName: map[string]*stream{}, sources: map[string]*source{}}
	for i, sl := range layout.Streams {
		s := newStream(e, i, sl)
		e.streams = append(e.streams, s)
		e.byName[sl.Stream.Name] = s
	}
	err = store.Query("SELECT source, seq, events, newest FROM "+SourcesTable, nil, func(row []any) error {
		name, _ := row[0].(string)
		src := &source{name: name, committed: asInt(row[1]), events: asInt(row[2]), newest: asInt(row[3])}
		src.applied = src.committed
		e.sources[name] = src
		if src.newest > e.now {
			e.now = src.newest
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range e.streams {
		if err := s.loadWatermarks(); err != nil {
			return nil, err
		}
		if err := s.loadExact(); err != nil {
			return nil, err
		}
		if s.clockSet && s.clock > e.now {
			e.now = s.clock
		}
	}
	return e, nil
}

// sameSchema reports whether two policies make the same tables and triggers.
func sameSchema(a, b *policy.Policy) bool {
	oa, err1 := compile.Compile(a, "")
	ob, err2 := compile.Compile(b, "")
	if err1 != nil || err2 != nil {
		return false
	}
	cut := func(s string) string {
		if i := strings.Index(s, "\n-- What this file holds"); i >= 0 {
			return s[:i]
		}
		return s
	}
	return cut(oa.Schema) == cut(ob.Schema)
}

// Policy is the policy the Engine runs.
func (e *Engine) Policy() *policy.Policy { return e.pol }

// Layout describes the Engine's file.
func (e *Engine) Layout() *compile.Layout { return e.layout }

// Store is the Engine's file.
func (e *Engine) Store() Store { return e.store }

// Stream returns the index of a stream, or -1.
func (e *Engine) Stream(name string) int {
	if s, ok := e.byName[name]; ok {
		return s.idx
	}
	return -1
}

// Key returns the id of a key tuple of a stream: text keys as strings, integer keys as int64.
func (e *Engine) Key(stream int, tuple ...any) (int32, error) {
	if stream < 0 || stream >= len(e.streams) {
		return 0, fmt.Errorf("no stream %d", stream)
	}
	return e.streams[stream].intern(tuple)
}

// KeyOf returns the key tuple behind a key id of a stream.
func (e *Engine) KeyOf(stream int, key int32) []any {
	if stream < 0 || stream >= len(e.streams) || int(key) < 0 || int(key) >= len(e.streams[stream].keys) {
		return nil
	}
	return e.streams[stream].keys[key]
}

// Committed returns the last sequence number from a source that the file holds.
func (e *Engine) Committed(src string) int64 {
	if s, ok := e.sources[src]; ok {
		return s.committed
	}
	return 0
}

// Applied returns the last sequence number from a source applied in memory.
func (e *Engine) Applied(src string) int64 {
	if s, ok := e.sources[src]; ok {
		return s.applied
	}
	return 0
}

// Now is the time of the newest event applied.
func (e *Engine) Now() int64 { return e.now }

// Stats returns counters since the Engine opened.
func (e *Engine) Stats() Stats { return e.stats }

// Resident is the number of windows held in memory.
func (e *Engine) Resident() int {
	n := 0
	for _, s := range e.streams {
		n += s.resident()
	}
	return n
}

func (e *Engine) source(name string) *source {
	s := e.sources[name]
	if s == nil {
		s = &source{name: name}
		e.sources[name] = s
	}
	return s
}

const maxExact = 1 << 53

// EventError is an event refused before it changed anything, such as one whose derived value
// divides by zero. The Engine carries on after it.
type EventError struct{ Msg string }

func (e *EventError) Error() string { return e.Msg }

// StoppedError means the Engine can no longer trust its memory: a read of its file failed part
// way through an event. Nothing more is applied; open a new Engine on the file and send again
// from the last committed sequence number.
type StoppedError struct{ Err error }

func (e *StoppedError) Error() string { return "engine stopped: " + e.Err.Error() }
func (e *StoppedError) Unwrap() error { return e.Err }

// Put applies one event. seq is the event's sequence number from its source, counted from 1;
// an event whose number is not above the last one applied from that source is skipped.
// With seq 0 the event is applied without the check. vals are the stream's values in policy order.
func (e *Engine) Put(stream int, src string, seq, ts int64, key int32, vals []float64) error {
	return e.PutID(stream, src, seq, ts, nil, key, vals)
}

// PutID applies one event of a stream whose events carry an identifier (an exact stream with an
// id line): text as a string, integer as an int64. An exact stream may refuse the event, as a
// repeat, as too late, or because its period has closed; the refusal is counted in the file and
// Put returns nil.
func (e *Engine) PutID(stream int, src string, seq, ts int64, id any, key int32, vals []float64) error {
	if e.broken != nil {
		return e.broken
	}
	ev := Event{Stream: stream, TS: ts, ID: id, Key: key, Vals: vals}
	s, err := e.check(&ev)
	if err != nil {
		return err
	}
	so := e.source(src)
	if seq > 0 && seq <= so.applied {
		e.stats.Duplicates++
		return nil
	}
	if err := e.apply(s, &ev); err != nil {
		return err
	}
	if seq > 0 {
		so.applied = seq
	}
	so.pending++
	so.changed = true
	if ts > so.newest {
		so.newest = ts
	}
	return nil
}

// Event is one event for PutEvents.
type Event struct {
	Stream int
	TS     int64
	ID     any // for an exact stream with an id line
	Key    int32
	Vals   []float64
	Line   string // for a stream from logs: the whole line
}

// PutEvents applies the events of one message under one sequence number, such as a log line that
// feeds several streams: all of them, or none when the number was already applied. An event that
// fails a check is skipped and the others are applied; the first failure is returned. The sender
// counts one event in the sources table for the message.
func (e *Engine) PutEvents(src string, seq int64, evs []Event) error {
	if e.broken != nil {
		return e.broken
	}
	so := e.source(src)
	if seq > 0 && seq <= so.applied {
		e.stats.Duplicates++
		return nil
	}
	var first error
	for i := range evs {
		ev := &evs[i]
		s, err := e.check(ev)
		if err == nil {
			err = e.apply(s, ev)
		}
		if err != nil {
			if e.broken != nil {
				return e.broken
			}
			if first == nil {
				first = err
			}
			continue
		}
		if ev.TS > so.newest {
			so.newest = ev.TS
		}
	}
	if seq > 0 {
		so.applied = seq
	}
	so.pending++
	so.changed = true
	return first
}

// check validates an event and puts its identifier in the stream's form.
func (e *Engine) check(ev *Event) (*stream, error) {
	if ev.Stream < 0 || ev.Stream >= len(e.streams) {
		return nil, fmt.Errorf("no stream %d", ev.Stream)
	}
	s := e.streams[ev.Stream]
	if f := s.pol.ID; f != nil {
		switch x := ev.ID.(type) {
		case string:
			if f.Type == "integer" {
				return nil, fmt.Errorf("%s: %s must be a whole number", s.pol.Name, f.Name)
			}
		case int64:
			if f.Type != "integer" {
				ev.ID = fmt.Sprint(x)
			}
		case int:
			ev.ID = int64(x)
			if f.Type != "integer" {
				ev.ID = fmt.Sprint(x)
			}
		case float64:
			if f.Type != "integer" || x != math.Trunc(x) {
				return nil, fmt.Errorf("%s: %s must be text or a whole number", s.pol.Name, f.Name)
			}
			ev.ID = int64(x)
		default:
			return nil, fmt.Errorf("%s: each event needs its %s", s.pol.Name, f.Name)
		}
	} else {
		ev.ID = nil
	}
	if int(ev.Key) < 0 || int(ev.Key) >= len(s.keys) {
		return nil, fmt.Errorf("%s: unknown key id %d", s.pol.Name, ev.Key)
	}
	if len(ev.Vals) != s.nvals {
		return nil, fmt.Errorf("%s: expected %d values, got %d", s.pol.Name, s.nvals, len(ev.Vals))
	}
	for i, v := range ev.Vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("%s: %s is not a finite number", s.pol.Name, s.pol.Values[i].Name)
		}
		if s.valInt[i] && (v != math.Trunc(v) || math.Abs(v) > maxExact) {
			return nil, fmt.Errorf("%s: %s must be a whole number", s.pol.Name, s.pol.Values[i].Name)
		}
	}
	return s, nil
}

// apply puts a checked event into its stream.
func (e *Engine) apply(s *stream, ev *Event) error {
	accepted, err := s.put(ev.TS, ev.ID, ev.Key, ev.Vals, ev.Line)
	if err != nil {
		var bad *EventError
		if errors.As(err, &bad) {
			return err
		}
		// A read from the file failed part way through an event: memory can no longer be trusted.
		e.broken = &StoppedError{Err: err}
		return e.broken
	}
	if !accepted {
		e.stats.Refused++
	}
	if ev.TS > e.now {
		e.now = ev.TS
	}
	e.stats.Events++
	return nil
}

// State is more of the file that the Engine writes at every checkpoint, in the same transaction
// as the rows, such as the templates the log reducer learns.
type State interface {
	// Blocks appends the rows that changed since the last checkpoint.
	Blocks(b *Batch)
	// Written is called once the checkpoint is in the file.
	Written()
}

// AddState makes a State part of every checkpoint.
func (e *Engine) AddState(st State) { e.states = append(e.states, st) }

// PutBatch applies events with consecutive sequence numbers starting at firstSeq (or no sequence
// numbers when firstSeq is 0). vals holds the values of each event one after another.
// It returns the number of events applied.
func (e *Engine) PutBatch(stream int, src string, firstSeq int64, ts []int64, keys []int32, vals []float64) (int, error) {
	if stream < 0 || stream >= len(e.streams) {
		return 0, fmt.Errorf("no stream %d", stream)
	}
	nv := e.streams[stream].nvals
	if len(keys) != len(ts) || len(vals) != len(ts)*nv {
		return 0, errors.New("batch columns have different lengths")
	}
	applied := 0
	for i := range ts {
		seq := int64(0)
		if firstSeq > 0 {
			seq = firstSeq + int64(i)
		}
		before := e.stats.Events
		if err := e.Put(stream, src, seq, ts[i], keys[i], vals[i*nv:(i+1)*nv]); err != nil {
			return applied, err
		}
		if e.stats.Events > before {
			applied++
		}
	}
	return applied, nil
}

// Checkpoint writes everything that changed since the last checkpoint to the file in one
// transaction, together with the last sequence number applied from each source, then runs the
// distill statements. When it returns without error, every event applied so far is in the file.
func (e *Engine) Checkpoint() error {
	if e.broken != nil {
		return e.broken
	}
	bt := &Batch{Distill: e.layout.Distill, Now: e.now}
	for _, s := range e.streams {
		s.blocks(bt, e.now)
	}
	for _, st := range e.states {
		st.Blocks(bt)
	}
	var sb *Block
	for _, so := range e.sources {
		if !so.changed {
			continue
		}
		if sb == nil {
			sb = newBlock("INSERT OR REPLACE INTO "+SourcesTable+" (source, seq, events, newest) VALUES (?, ?, ?, ?)", []byte("tiii"))
		}
		sb.text(so.name)
		sb.int(so.applied)
		sb.int(so.events + so.pending)
		sb.int(so.newest)
		sb.row()
	}
	if sb != nil {
		bt.Blocks = append(bt.Blocks, sb)
	}
	if len(bt.Blocks) == 0 {
		return nil
	}
	if e.now == 0 {
		bt.Distill = nil
	}
	if err := e.store.Apply(bt); err != nil {
		return err
	}
	for _, b := range bt.Blocks {
		e.stats.RowsWritten += int64(b.N)
	}
	e.stats.Checkpoints++
	for _, s := range e.streams {
		s.written(e.now)
	}
	for _, st := range e.states {
		st.Written()
	}
	for _, so := range e.sources {
		if so.changed {
			so.committed = so.applied
			so.events += so.pending
			so.pending = 0
			so.changed = false
		}
	}
	return nil
}

// Pending is the number of events applied in memory but not yet in the file.
func (e *Engine) Pending() int64 {
	var n int64
	for _, so := range e.sources {
		n += so.pending
	}
	return n
}
