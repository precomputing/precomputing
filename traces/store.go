package traces

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"

	"precomputing.com/precomputing/engine"
)

// The tables the store adds to the Engine's file, beside the policy's own. trace_requests rebuilds
// every call from its pieces with SQL alone.
const schema = `
CREATE TABLE IF NOT EXISTS trace_pieces (
  id      INTEGER PRIMARY KEY,   -- numbered in the order the pieces were first seen
  sha256  TEXT NOT NULL UNIQUE,  -- of body
  source  TEXT NOT NULL,         -- system, tools, task, user, assistant, or tool:NAME
  tokens  INTEGER NOT NULL,
  bytes   INTEGER NOT NULL,
  body    TEXT NOT NULL          -- the message or tool list as sent, with secrets masked
);
CREATE TABLE IF NOT EXISTS trace_calls (
  call_id        TEXT PRIMARY KEY,  -- the run and the call's number, such as run-17#4
  run            TEXT NOT NULL,
  seq            INTEGER NOT NULL,
  ts             INTEGER NOT NULL,  -- when the reply came back
  repo           TEXT NOT NULL,
  model          TEXT NOT NULL,
  messages       TEXT NOT NULL,     -- the request's messages: a JSON array of piece ids, in order
  tools          INTEGER NOT NULL,  -- the piece id of the tool list
  reply          INTEGER NOT NULL,  -- the piece id of the reply
  request_bytes  INTEGER NOT NULL,
  request_sha256 TEXT NOT NULL,     -- of the request as sent, with secrets masked
  input_tokens   INTEGER NOT NULL,
  cached_tokens  INTEGER NOT NULL,
  output_tokens  INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS trace_calls_run ON trace_calls (run, seq);
CREATE VIEW IF NOT EXISTS trace_requests AS
SELECT c.call_id, c.run, c.seq,
  '{"model":' || json_quote(c.model) || ',"messages":[' ||
  coalesce((SELECT group_concat(p.body, ',' ORDER BY j.key) FROM json_each(c.messages) AS j JOIN trace_pieces AS p ON p.id = j.value), '') ||
  '],"tools":' || (SELECT body FROM trace_pieces WHERE id = c.tools) || '}' AS request,
  (SELECT body FROM trace_pieces WHERE id = c.reply) AS reply
FROM trace_calls AS c;
`

// Stats counts what a store has done since it opened.
type Stats struct {
	Calls        int64 // calls stored
	Repeats      int64 // calls that were already stored: their events go to the streams, which refuse them
	Pieces       int64 // new pieces stored
	PieceBytes   int64 // bytes of the new pieces
	RequestBytes int64 // bytes of every request as sent: what storing each call whole would take
	ReplyBytes   int64
	Masked       int64 // secrets masked
}

type piece struct {
	id     int64
	sum    string
	source string
	tokens int64
	body   string
}

type callRow struct {
	id, run, repo, model, messages, sum string
	seq                                 int
	ts, tools, reply, bytes             int64
	u                                   Usage
}

// runState remembers what a run's earlier calls already did: the pieces of its messages, and the
// SHA-256 state of its request up to the last message, so that each call adds only what is new.
type runState struct {
	toolsID   int64 // the tool list, masked once for the run
	tools     string
	replyAt   int // the last reply, which the next call sends as input
	replyID   int64
	replyBody string
	ids       []int64
	h         hash.Hash
	hashed    int   // messages written into h
	prefix    int64 // bytes written into h
	idsJSON   strings.Builder
}

// Store keeps a policy's agent calls in the Engine's file. It is an engine.State: what it adds
// goes into the file with the Engine's checkpoints, in the same transaction as the meter's rows.
type Store struct {
	eng            *engine.Engine
	calls, context int
	callKeys       map[string]int32
	ctxKeys        map[string]int32
	ids            map[string]int64 // sha256 -> piece id
	next           int64
	known          map[string]bool // calls already stored
	runs           map[string]*runState
	pieces         []piece
	rows           []callRow
	stats          Stats
}

// Open starts a store on an Engine whose policy has the streams calls and context
// (examples/traces.precompute). It creates its tables when the file has none.
func Open(eng *engine.Engine) (*Store, error) {
	s := &Store{eng: eng, calls: eng.Stream("calls"), context: eng.Stream("context"), callKeys: map[string]int32{}, ctxKeys: map[string]int32{},
		ids: map[string]int64{}, known: map[string]bool{}, runs: map[string]*runState{}, next: 1}
	if s.calls < 0 || s.context < 0 {
		return nil, fmt.Errorf("traces need a policy with the streams calls and context, as in examples/traces.precompute")
	}
	for _, st := range eng.Policy().Streams {
		var keys, vals []string
		for _, k := range st.Keys {
			keys = append(keys, k.Name)
		}
		for _, v := range st.Values {
			vals = append(vals, v.Name)
		}
		want := map[string][2]string{"calls": {"repo run model", "input_tokens cached_tokens output_tokens"}, "context": {"repo source", "tokens cached_tokens output_tokens"}}[st.Name]
		if want[0] != "" && (strings.Join(keys, " ") != want[0] || strings.Join(vals, " ") != want[1] || st.ID == nil) {
			return nil, fmt.Errorf("stream %s must have an id, the keys %s and the values %s", st.Name, want[0], want[1])
		}
	}
	if err := eng.Store().Exec(schema); err != nil {
		return nil, err
	}
	err := eng.Store().Query("SELECT id, sha256 FROM trace_pieces", nil, func(row []any) error {
		id := asInt(row[0])
		s.ids[fmt.Sprint(row[1])] = id
		if id >= s.next {
			s.next = id + 1
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := eng.Store().Query("SELECT call_id FROM trace_calls", nil, func(row []any) error {
		s.known[fmt.Sprint(row[0])] = true
		return nil
	}); err != nil {
		return nil, err
	}
	eng.AddState(s)
	return s, nil
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

// Stats returns counters since the store opened.
func (s *Store) Stats() Stats { return s.stats }

// piece stores a piece once and returns its id and its masked body.
func (s *Store) piece(raw, source string, tokens int64) (int64, string) {
	body, n := Redact(raw)
	s.stats.Masked += int64(n)
	sum := sha256.Sum256([]byte(body))
	key := hex.EncodeToString(sum[:])
	if id, ok := s.ids[key]; ok {
		return id, body
	}
	id := s.next
	s.next++
	s.ids[key] = id
	s.pieces = append(s.pieces, piece{id, key, source, tokens, body})
	s.stats.Pieces++
	s.stats.PieceBytes += int64(len(body))
	return id, body
}

func (s *Store) key(cache map[string]int32, stream int, tuple ...any) (int32, error) {
	k := fmt.Sprint(tuple...)
	if id, ok := cache[k]; ok {
		return id, nil
	}
	id, err := s.eng.Key(stream, tuple...)
	if err != nil {
		return 0, err
	}
	cache[k] = id
	return id, nil
}

// Put stores call number seq of a run (from 1) and meters it. The run's calls must come in order.
// A call already stored is metered again, and the exact streams refuse it as a repeat.
func (s *Store) Put(r *Run, seq int) error {
	replies := r.Replies()
	if seq < 1 || seq > len(replies) {
		return fmt.Errorf("run %s has no call %d", r.ID, seq)
	}
	i := replies[seq-1]
	id := r.CallID(seq)
	u := r.UsageOf(i)
	if s.known[id] {
		s.stats.Repeats++
		return s.meter(r, id, u)
	}
	st := s.runs[r.ID]
	if st == nil {
		st = &runState{h: sha256.New(), replyAt: -1}
		head := `{"model":` + quote(r.Model) + `,"messages":[`
		st.h.Write([]byte(head))
		st.prefix = int64(len(head))
		st.idsJSON.WriteByte('[')
		s.runs[r.ID] = st
	}
	// Add the messages this call sends that earlier calls did not.
	for j := st.hashed; j < i; j++ {
		m := r.Messages[j]
		var pid int64
		var body string
		if j == st.replyAt && st.replyBody != "" {
			pid, body = st.replyID, st.replyBody
		} else {
			pid, body = s.piece(m.Raw, m.Source, m.Tokens)
		}
		if j > 0 {
			st.h.Write([]byte{','})
			st.prefix++
			st.idsJSON.WriteByte(',')
		}
		st.h.Write([]byte(body))
		st.prefix += int64(len(body))
		st.ids = append(st.ids, pid)
		st.idsJSON.WriteString(strconv.FormatInt(pid, 10))
		st.hashed++
	}
	if st.hashed != i {
		return fmt.Errorf("run %s: call %d came before the call it follows", r.ID, seq)
	}
	if st.tools == "" {
		st.toolsID, st.tools = s.piece(r.Tools, "tools", r.ToolsTokens)
	}
	toolsID, tools := st.toolsID, st.tools
	replyID, reply := s.piece(r.Messages[i].Raw, r.Messages[i].Source, r.Messages[i].Tokens)
	st.replyAt, st.replyID, st.replyBody = i, replyID, reply
	// Finish the request's hash on a copy of the state, so the next call carries on from here.
	h := sha256.New()
	state, _ := st.h.(encoding.BinaryMarshaler).MarshalBinary()
	h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state)
	h.Write([]byte(`],"tools":`))
	h.Write([]byte(tools))
	h.Write([]byte{'}'})
	size := st.prefix + int64(len(`],"tools":`)+len(tools)+1)
	s.rows = append(s.rows, callRow{id: id, run: r.ID, repo: r.Repo, model: r.Model, messages: st.idsJSON.String() + "]",
		sum: hex.EncodeToString(h.Sum(nil)), seq: seq, ts: u.TS, tools: toolsID, reply: replyID, bytes: size, u: u})
	s.known[id] = true
	s.stats.Calls++
	s.stats.RequestBytes += size
	s.stats.ReplyBytes += int64(len(reply))
	if seq == len(replies) {
		delete(s.runs, r.ID) // the run is over
	}
	return s.meter(r, id, u)
}

// meter hands one call to the exact streams: one event for the call, one for each source of its
// input, and one for its output.
func (s *Store) meter(r *Run, id string, u Usage) error {
	k, err := s.key(s.callKeys, s.calls, r.Repo, r.ID, r.Model)
	if err != nil {
		return err
	}
	if err := s.eng.PutID(s.calls, "traces", 0, u.TS, id, k, []float64{float64(u.Input), float64(u.Cached), float64(u.Output)}); err != nil {
		return err
	}
	srcs := make([]string, 0, len(u.Sources))
	for src := range u.Sources {
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)
	for _, src := range srcs {
		t := u.Sources[src]
		k, err := s.key(s.ctxKeys, s.context, r.Repo, src)
		if err != nil {
			return err
		}
		if err := s.eng.PutID(s.context, "traces", 0, u.TS, id+"/"+src, k, []float64{float64(t[0]), float64(t[1]), 0}); err != nil {
			return err
		}
	}
	k, err = s.key(s.ctxKeys, s.context, r.Repo, "output")
	if err != nil {
		return err
	}
	return s.eng.PutID(s.context, "traces", 0, u.TS, id+"/output", k, []float64{0, 0, float64(u.Output)})
}

// Blocks writes the pieces and calls stored since the last checkpoint.
func (s *Store) Blocks(bt *engine.Batch) {
	if len(s.pieces) > 0 {
		b := engine.NewBlock("INSERT OR IGNORE INTO trace_pieces (id, sha256, source, tokens, bytes, body) VALUES (?, ?, ?, ?, ?, ?)", "ittiit")
		for _, p := range s.pieces {
			b.AddInt(p.id)
			b.AddText(p.sum)
			b.AddText(p.source)
			b.AddInt(p.tokens)
			b.AddInt(int64(len(p.body)))
			b.AddText(p.body)
			b.EndRow()
		}
		bt.Blocks = append(bt.Blocks, b)
	}
	if len(s.rows) > 0 {
		b := engine.NewBlock("INSERT OR IGNORE INTO trace_calls (call_id, run, seq, ts, repo, model, messages, tools, reply, request_bytes, request_sha256, input_tokens, cached_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", "ttiitttiiitiii")
		for _, c := range s.rows {
			b.AddText(c.id)
			b.AddText(c.run)
			b.AddInt(int64(c.seq))
			b.AddInt(c.ts)
			b.AddText(c.repo)
			b.AddText(c.model)
			b.AddText(c.messages)
			b.AddInt(c.tools)
			b.AddInt(c.reply)
			b.AddInt(c.bytes)
			b.AddText(c.sum)
			b.AddInt(c.u.Input)
			b.AddInt(c.u.Cached)
			b.AddInt(c.u.Output)
			b.EndRow()
		}
		bt.Blocks = append(bt.Blocks, b)
	}
}

// Written is called once a checkpoint is in the file.
func (s *Store) Written() {
	s.pieces = s.pieces[:0]
	s.rows = s.rows[:0]
}

// Rebuild reads a call back from the file: the request as it was sent, with secrets masked, and
// the reply. It uses nothing but the file and the trace_requests view.
func Rebuild(st engine.Store, callID string) (request, reply string, err error) {
	found := false
	err = st.Query("SELECT request, reply FROM trace_requests WHERE call_id = ?", []any{callID}, func(row []any) error {
		request, _ = row[0].(string)
		reply, _ = row[1].(string)
		found = true
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("the file has no call %s", callID)
	}
	return request, reply, err
}
