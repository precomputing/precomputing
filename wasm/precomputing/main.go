//go:build js && wasm

// Command precomputing is the compiler and the Engine built for the browser, in one file.
// Starting it hands an object to globalThis.__precomputingReady (when set):
//
//	compile(text, name)             -> {schema, distill, error}
//	open(store, policyText, name)   -> an engine, or {error}
//	logs(store, policyText, name)   -> an engine with a log reducer, or {error}
//	importDashboard(json)           -> {policy, plans} or {error}
//	mcp(reader, title)              -> an MCP server for one file: handle(body, headers) -> {status, body}
//	traces(store, policyText, name) -> an engine with a store of agent calls in front of it, or {error}
//	version                         the version string
//
// It also sets globalThis.precomputingCompile and globalThis.precomputingImport for pages that only
// need the compiler or the dashboard import.
//
// The store is the page's SQLite (the WebAssembly build) seen through three functions:
//
//	exec(sql)          -> null, or an error message
//	query(sql, args)   -> {rows: [[...], ...]} or {error}; numbers as JavaScript numbers, not BigInt
//	apply(bytes)       -> null, or an error message; bytes is a checkpoint (see encodeBatch)
//
// An MCP server reads its file through a reader, one function over the page's SQLite:
//
//	read(sql, args, max) -> {columns: [...], rows: [[...], ...], more} or {error}
//
// The Engine uses Go's math.Log for ln(): it gives the same bits as ln() in SQLite's
// WebAssembly build, which takes its logarithm from the same FreeBSD-derived code.
package main

import (
	"encoding/binary"
	"errors"
	"math"
	"syscall/js"
	"time"

	"encoding/json"
	"strings"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/logs"
	"precomputing.com/precomputing/mcp"
	"precomputing.com/precomputing/policy"
	"precomputing.com/precomputing/traces"
)

func main() {
	api := js.Global().Get("Object").New()
	compileFn := js.FuncOf(func(this js.Value, args []js.Value) any {
		text, name := "", "policy.precompute"
		if len(args) > 0 {
			text = args[0].String()
		}
		if len(args) > 1 && args[1].Type() == js.TypeString {
			name = args[1].String()
		}
		res := map[string]any{"schema": "", "distill": "", "error": ""}
		out, err := compile.Source(text, name)
		if err != nil {
			res["error"] = err.Error()
		} else {
			res["schema"], res["distill"] = out.Schema, out.Distill
		}
		return js.ValueOf(res)
	})
	importFn := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			return errObj(errors.New("importDashboard needs the dashboard's JSON"))
		}
		imp, err := logs.Import([]byte(args[0].String()))
		if err != nil {
			return errObj(err)
		}
		b, _ := json.Marshal(imp)
		return js.Global().Get("JSON").Call("parse", string(b))
	})
	api.Set("compile", compileFn)
	api.Set("version", version.Version)
	api.Set("open", js.FuncOf(open))
	api.Set("logs", js.FuncOf(openLogs))
	api.Set("importDashboard", importFn)
	api.Set("mcp", js.FuncOf(openMCP))
	api.Set("traces", js.FuncOf(openTraces))
	js.Global().Set("precomputingCompile", compileFn)
	js.Global().Set("precomputingImport", importFn)
	js.Global().Set("precomputingVersion", version.Version)
	if ready := js.Global().Get("__precomputingReady"); ready.Type() == js.TypeFunction {
		ready.Invoke(api)
	}
	select {}
}

func errObj(err error) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("error", err.Error())
	return o
}

// open(store, policyText or null, name)
func open(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errObj(errors.New("open needs a store"))
	}
	st := &jsStore{v: args[0]}
	var pol *policy.Policy
	name := "policy.precompute"
	if len(args) > 1 && args[1].Type() == js.TypeString {
		p, err := policy.Parse(args[1].String())
		if err != nil {
			return errObj(err)
		}
		pol = p
	}
	if len(args) > 2 && args[2].Type() == js.TypeString {
		name = args[2].String()
	}
	eng, err := engine.Open(st, pol, name)
	if err != nil {
		return errObj(err)
	}
	return engineObject(eng, st)
}

// openLogs(store, policyText or null, name): an Engine whose policy has a logs block, with the
// log reducer in front of it. On top of the engine's functions:
//
//	putLines(text, source, firstSeq) -> {lines, read, notRead, events, repeats, newTemplates: [...]}
//	templates()                      -> [{id, service, level, text, n, firstTs, lastTs, example}]
//
// text holds lines separated by "\n"; line i gets the sequence number firstSeq + i.
func openLogs(this js.Value, args []js.Value) any {
	o := open(this, args)
	v, ok := o.(js.Value)
	if !ok || v.Get("error").Truthy() {
		return o
	}
	eng := openEngines[len(openEngines)-1]
	red, err := logs.New(eng)
	if err != nil {
		return errObj(err)
	}
	tmpl := func(t *logs.Template) map[string]any {
		return map[string]any{"id": t.ID, "service": t.Service, "level": t.Level, "text": t.Text(), "n": t.Count(),
			"firstTs": t.FirstTS, "lastTs": t.LastTS, "example": t.Example}
	}
	putLines := js.FuncOf(func(this js.Value, a []js.Value) any {
		text, src, first := a[0].String(), a[1].String(), int64(a[2].Float())
		var lines []string
		if text != "" {
			lines = strings.Split(text, "\n")
		}
		res := map[string]any{}
		var news []any
		read, notRead, events, repeats := 0, 0, 0, 0
		for i, line := range lines {
			seq := int64(0)
			if first > 0 {
				seq = first + int64(i)
			}
			r, err := red.Put(src, seq, line)
			if err != nil {
				var stopped *engine.StoppedError
				if errors.As(err, &stopped) {
					res["error"] = err.Error()
					break
				}
			}
			switch {
			case r.Repeat:
				repeats++
			case r.Read:
				read++
				events += r.Events
				if r.New {
					news = append(news, tmpl(r.Template))
				}
			default:
				notRead++
			}
		}
		res["lines"], res["read"], res["notRead"], res["events"], res["repeats"] = len(lines), read, notRead, events, repeats
		res["newTemplates"] = news
		return js.ValueOf(res)
	})
	templates := js.FuncOf(func(this js.Value, a []js.Value) any {
		var out []any
		for _, t := range red.Templates() {
			out = append(out, tmpl(t))
		}
		return js.ValueOf(out)
	})
	v.Set("putLines", putLines)
	v.Set("templates", templates)
	return v
}

// openTraces(store, policyText or null, name): an Engine whose policy has the streams calls and
// context (examples/traces.precompute), with the store of agent calls in front of it. On top of
// the engine's functions:
//
//	load(text)    -> {runs, calls, first, last} or {error}: runs, one JSON line each
//	until(t)      -> {put, next, done} or {error}: hands over every call whose reply came back by t
//	again(run)    -> {calls} or {error}: reports the calls of a run already stored once more
//	traceStats()  -> {calls, repeats, pieces, pieceBytes, requestBytes, replyBytes, masked}
//	patterns()    -> [{name, re, keep, label}]: the secrets masked before anything is stored
func openTraces(this js.Value, args []js.Value) any {
	o := open(this, args)
	v, ok := o.(js.Value)
	if !ok || v.Get("error").Truthy() {
		return o
	}
	eng := openEngines[len(openEngines)-1]
	store, err := traces.Open(eng)
	if err != nil {
		return errObj(err)
	}
	var rp *traces.Replay
	v.Set("load", js.FuncOf(func(this js.Value, a []js.Value) any {
		day, err := traces.ReadDay(strings.NewReader(a[0].String()))
		if err != nil {
			return errObj(err)
		}
		rp = &traces.Replay{Day: day, Store: store}
		res := map[string]any{"runs": len(day.Runs), "calls": len(day.Calls), "first": 0, "last": 0}
		if n := len(day.Calls); n > 0 {
			res["first"], res["last"] = day.Calls[0].TS, day.Calls[n-1].TS
		}
		return js.ValueOf(res)
	}))
	v.Set("until", js.FuncOf(func(this js.Value, a []js.Value) any {
		if rp == nil {
			return errObj(errors.New("load the runs first"))
		}
		n, err := rp.Until(int64(a[0].Float()))
		res := map[string]any{"put": n, "next": rp.Next, "done": rp.Done()}
		if err != nil {
			res["error"] = err.Error()
		}
		return js.ValueOf(res)
	}))
	v.Set("again", js.FuncOf(func(this js.Value, a []js.Value) any {
		if rp == nil {
			return errObj(errors.New("load the runs first"))
		}
		r := rp.Day.Run(a[0].String())
		if r == nil {
			return errObj(errors.New("no run " + a[0].String()))
		}
		n, err := store.Again(r)
		if err != nil {
			return errObj(err)
		}
		return js.ValueOf(map[string]any{"calls": n})
	}))
	v.Set("traceStats", js.FuncOf(func(this js.Value, a []js.Value) any {
		s := store.Stats()
		return js.ValueOf(map[string]any{"calls": s.Calls, "repeats": s.Repeats, "pieces": s.Pieces, "pieceBytes": s.PieceBytes,
			"requestBytes": s.RequestBytes, "replyBytes": s.ReplyBytes, "masked": s.Masked})
	}))
	v.Set("patterns", js.FuncOf(func(this js.Value, a []js.Value) any {
		var out []any
		for _, p := range traces.Patterns {
			out = append(out, map[string]any{"name": p.Name, "re": p.Re, "keep": p.Keep, "label": p.Label()})
		}
		return js.ValueOf(out)
	}))
	return v
}

// openEngines remembers the Engines opened, so that openLogs can put a reducer in front of one.
var openEngines []*engine.Engine

func engineObject(eng *engine.Engine, st *jsStore) js.Value {
	openEngines = append(openEngines, eng)
	o := js.Global().Get("Object").New()
	var funcs []js.Func
	fn := func(name string, f func(args []js.Value) any) {
		jf := js.FuncOf(func(this js.Value, args []js.Value) any { return f(args) })
		funcs = append(funcs, jf)
		o.Set(name, jf)
	}
	fn("stream", func(a []js.Value) any { return eng.Stream(a[0].String()) })
	// key(stream, k1, k2, ...) -> id, or -1 with keyError set
	fn("key", func(a []js.Value) any {
		var tuple []any
		for _, v := range a[1:] {
			if v.Type() == js.TypeString {
				tuple = append(tuple, v.String())
			} else {
				tuple = append(tuple, v.Float())
			}
		}
		id, err := eng.Key(a[0].Int(), tuple...)
		if err != nil {
			o.Set("keyError", err.Error())
			return -1
		}
		return id
	})
	// put(stream, source, firstSeq, n, bytes): bytes holds n rows of doubles: ts, key id, then each value.
	var buf []byte
	var ts []int64
	var keys []int32
	var vals []float64
	fn("put", func(a []js.Value) any {
		stream, src, first, n := a[0].Int(), a[1].String(), int64(a[2].Float()), a[3].Int()
		u8 := a[4]
		size := u8.Get("byteLength").Int()
		if cap(buf) < size {
			buf = make([]byte, size)
		}
		buf = buf[:size]
		js.CopyBytesToGo(buf, u8)
		width := size / 8 / max(n, 1)
		nv := width - 2
		ts, keys, vals = ts[:0], keys[:0], vals[:0]
		for i := 0; i < n; i++ {
			row := buf[i*width*8:]
			ts = append(ts, int64(f64(row, 0)))
			keys = append(keys, int32(f64(row, 1)))
			for j := 0; j < nv; j++ {
				vals = append(vals, f64(row, 2+j))
			}
		}
		res := js.Global().Get("Object").New()
		applied, err := eng.PutBatch(stream, src, first, ts, keys, vals)
		res.Set("applied", applied)
		if err != nil {
			res.Set("error", err.Error())
		}
		return res
	})
	fn("checkpoint", func(a []js.Value) any {
		res := js.Global().Get("Object").New()
		before := eng.Stats().RowsWritten
		start := time.Now()
		if err := eng.Checkpoint(); err != nil {
			res.Set("error", err.Error())
		}
		res.Set("rows", eng.Stats().RowsWritten-before)
		res.Set("ms", float64(time.Since(start).Microseconds())/1000)
		res.Set("bytes", st.lastBytes)
		return res
	})
	fn("committed", func(a []js.Value) any { return eng.Committed(a[0].String()) })
	fn("applied", func(a []js.Value) any { return eng.Applied(a[0].String()) })
	fn("now", func(a []js.Value) any { return eng.Now() })
	fn("pending", func(a []js.Value) any { return eng.Pending() })
	fn("resident", func(a []js.Value) any { return eng.Resident() })
	// close lets the engine go: its functions stop working and its memory can be reclaimed.
	fn("close", func(a []js.Value) any {
		for _, f := range funcs {
			f.Release()
		}
		for i, e := range openEngines {
			if e == eng {
				openEngines = append(openEngines[:i], openEngines[i+1:]...)
				break
			}
		}
		return nil
	})
	fn("stats", func(a []js.Value) any {
		s := eng.Stats()
		return js.ValueOf(map[string]any{
			"events": s.Events, "duplicates": s.Duplicates, "checkpoints": s.Checkpoints,
			"rowsWritten": s.RowsWritten, "reads": s.Reads,
		})
	})
	return o
}

func f64(b []byte, i int) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:])) }

// jsStore is the page's SQLite.
type jsStore struct {
	v         js.Value
	lastBytes int
}

func (s *jsStore) Exec(sql string) error {
	if r := s.v.Call("exec", sql); r.Truthy() {
		return errors.New(r.String())
	}
	return nil
}

func (s *jsStore) Query(sql string, args []any, fn func([]any) error) error {
	jargs := make([]any, len(args))
	for i, a := range args {
		switch x := a.(type) {
		case int64:
			jargs[i] = float64(x)
		case int:
			jargs[i] = float64(x)
		default:
			jargs[i] = x
		}
	}
	r := s.v.Call("query", sql, js.ValueOf(jargs))
	if e := r.Get("error"); e.Truthy() {
		return errors.New(e.String())
	}
	rows := r.Get("rows")
	n := rows.Length()
	for i := 0; i < n; i++ {
		jr := rows.Index(i)
		row := make([]any, jr.Length())
		for j := range row {
			c := jr.Index(j)
			switch c.Type() {
			case js.TypeNumber:
				row[j] = c.Float()
			case js.TypeString:
				row[j] = c.String()
			default:
				row[j] = nil
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

func (s *jsStore) Log(x float64) float64 { return math.Log(x) }

func (s *jsStore) Apply(b *engine.Batch) error {
	enc := encodeBatch(b)
	s.lastBytes = len(enc)
	u8 := js.Global().Get("Uint8Array").New(len(enc))
	js.CopyBytesToJS(u8, enc)
	if r := s.v.Call("apply", u8); r.Truthy() {
		return errors.New(r.String())
	}
	return nil
}

// encodeBatch lays out a checkpoint for the JavaScript side, little-endian, every array aligned
// to its element size so it can be read in place:
//
//	"PCB1", u32 blocks, u32 distill statements, u32 0, f64 now
//	per block: str sql, str types, u32 rows, f64s ints, f64s reals, str text, i32s offs, i32s lens
//	per distill statement: str sql
//
// where str is u32 length, bytes, zero padding to 4; f64s is u32 count, padding to 8, doubles;
// i32s is u32 count, int32s. Integers travel as doubles, exact below 2^53.
func encodeBatch(b *engine.Batch) []byte {
	var out []byte
	u32 := func(v int) { out = binary.LittleEndian.AppendUint32(out, uint32(v)) }
	pad := func(to int) {
		for len(out)%to != 0 {
			out = append(out, 0)
		}
	}
	str := func(s []byte) {
		u32(len(s))
		out = append(out, s...)
		pad(4)
	}
	f64s := func(v []float64) {
		u32(len(v))
		pad(8)
		for _, x := range v {
			out = binary.LittleEndian.AppendUint64(out, math.Float64bits(x))
		}
	}
	i32s := func(v []int32) {
		u32(len(v))
		for _, x := range v {
			out = binary.LittleEndian.AppendUint32(out, uint32(x))
		}
	}
	out = append(out, "PCB1"...)
	u32(len(b.Blocks))
	u32(len(b.Distill))
	u32(0)
	out = binary.LittleEndian.AppendUint64(out, math.Float64bits(float64(b.Now)))
	ints := []float64{}
	for _, blk := range b.Blocks {
		str([]byte(blk.SQL))
		str(blk.Types)
		u32(blk.N)
		ints = ints[:0]
		for _, v := range blk.Ints {
			ints = append(ints, float64(v))
		}
		f64s(ints)
		f64s(blk.Reals)
		str(blk.Text)
		i32s(blk.Offs)
		i32s(blk.Lens)
	}
	for _, d := range b.Distill {
		str([]byte(d))
	}
	return out
}

// openMCP(reader, title): the MCP server of the native build, answering one JSON-RPC message at a
// time. headers is an object of the request's HTTP headers, or null for stdio semantics.
func openMCP(this js.Value, args []js.Value) any {
	if len(args) < 1 || args[0].Type() != js.TypeObject {
		return errObj(errors.New("mcp needs a reader"))
	}
	title := "Precomputing"
	if len(args) > 1 && args[1].Type() == js.TypeString {
		title = args[1].String()
	}
	srv := &mcp.Server{Reader: &jsReader{v: args[0]}, Name: "precomputing", Title: title, Version: version.Version}
	o := js.Global().Get("Object").New()
	o.Set("handle", js.FuncOf(func(this js.Value, a []js.Value) any {
		if len(a) < 1 {
			return errObj(errors.New("handle needs a message"))
		}
		var h mcp.Headers
		if len(a) > 1 && a[1].Type() == js.TypeObject {
			m := map[string]string{}
			keys := js.Global().Get("Object").Call("keys", a[1])
			for i := 0; i < keys.Length(); i++ {
				k := keys.Index(i).String()
				m[strings.ToLower(k)] = a[1].Get(k).String()
			}
			h = func(name string) string { return m[strings.ToLower(name)] }
		}
		status, body := srv.Handle([]byte(a[0].String()), h)
		res := js.Global().Get("Object").New()
		res.Set("status", status)
		res.Set("body", string(body))
		return res
	}))
	return o
}

// jsReader reads a file through the page's read function.
type jsReader struct{ v js.Value }

func (r *jsReader) Read(sql string, args []any, max int) (*mcp.Rows, error) {
	if err := mcp.SingleStatement(sql); err != nil {
		return nil, err
	}
	jargs := make([]any, len(args))
	for i, a := range args {
		switch x := a.(type) {
		case int64:
			jargs[i] = float64(x)
		case int:
			jargs[i] = float64(x)
		default:
			jargs[i] = x
		}
	}
	res := r.v.Call("read", sql, js.ValueOf(jargs), max)
	if e := res.Get("error"); e.Truthy() {
		return nil, errors.New(e.String())
	}
	out := &mcp.Rows{More: res.Get("more").Truthy()}
	cols := res.Get("columns")
	for i := 0; i < cols.Length(); i++ {
		out.Columns = append(out.Columns, cols.Index(i).String())
	}
	rows := res.Get("rows")
	for i := 0; i < rows.Length(); i++ {
		jr := rows.Index(i)
		row := make([]any, jr.Length())
		for j := range row {
			c := jr.Index(j)
			switch c.Type() {
			case js.TypeNumber:
				row[j] = c.Float()
			case js.TypeString:
				row[j] = c.String()
			default:
				row[j] = nil
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
