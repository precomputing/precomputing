package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/policy"
)

// openEngine opens the file at path with the Engine. With a policy path, a new file is made from
// it (an existing file must hold the same policy); without one, the file's own policy is used.
func openEngine(path, policyPath, sync string) (*engine.Engine, *sqlitestore.Store, error) {
	var pol *policy.Policy
	name := "policy.precompute"
	if policyPath != "" {
		src, err := os.ReadFile(policyPath)
		if err != nil {
			return nil, nil, err
		}
		if pol, err = policy.Parse(string(src)); err != nil {
			return nil, nil, fmt.Errorf("%s:%v", policyPath, err)
		}
		name = baseName(policyPath)
	} else if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("%s does not exist; give --policy to make it", path)
	}
	st, err := sqlitestore.Open(path, sqlitestore.Options{Sync: sync})
	if err != nil {
		return nil, nil, err
	}
	eng, err := engine.Open(st, pol, name)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return eng, st, nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// event is one parsed input line.
type event struct {
	stream int
	seq    int64
	ts     int64
	id     any // the event's identifier, for exact streams that have one
	key    int32
	vals   []float64
}

// parser reads events from lines of CSV or JSON.
//
// CSV: [seq,] ts, [id,] keys..., values... in the order the policy declares them, one event per line,
// for the stream given with --stream (or the policy's only stream).
// JSON: one object per line, fields by name: {"stream": "trades", "seq": 1, "ts": 1790602200,
// "symbol": "SIM1", "price": 187.41, "size": 100}; stream may be left out when there is one.
type parser struct {
	eng    *engine.Engine
	json   bool
	seqCol bool
	stream int
	keys   map[string]int32
}

func newParser(eng *engine.Engine, format, stream string, seqCol bool) (*parser, error) {
	p := &parser{eng: eng, json: format == "json", seqCol: seqCol, stream: -1, keys: map[string]int32{}}
	if format != "csv" && format != "json" {
		return nil, fmt.Errorf("--format is csv or json, not %q", format)
	}
	streams := eng.Policy().Streams
	switch {
	case stream != "":
		if p.stream = eng.Stream(stream); p.stream < 0 {
			return nil, fmt.Errorf("the policy has no stream %q", stream)
		}
	case len(streams) == 1:
		p.stream = 0
	case !p.json:
		return nil, fmt.Errorf("the policy has %d streams: give --stream, or send JSON lines with a stream field", len(streams))
	}
	return p, nil
}

func (p *parser) parse(line []byte) (event, error) {
	if p.json {
		return p.parseJSON(line)
	}
	return p.parseCSV(line)
}

func (p *parser) parseCSV(line []byte) (event, error) {
	s := p.eng.Policy().Streams[p.stream]
	fields := strings.Split(strings.TrimSpace(string(line)), ",")
	want := 1 + len(s.Keys) + len(s.Values)
	if p.seqCol {
		want++
	}
	if s.ID != nil {
		want++
	}
	if len(fields) != want {
		return event{}, fmt.Errorf("expected %d fields, found %d", want, len(fields))
	}
	ev := event{stream: p.stream}
	i := 0
	var err error
	if p.seqCol {
		if ev.seq, err = strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64); err != nil || ev.seq < 1 {
			return event{}, fmt.Errorf("sequence number %q is not a whole number from 1", fields[0])
		}
		i++
	}
	if ev.ts, err = strconv.ParseInt(strings.TrimSpace(fields[i]), 10, 64); err != nil {
		return event{}, fmt.Errorf("time %q is not a whole number of seconds", fields[i])
	}
	i++
	if f := s.ID; f != nil {
		v := strings.TrimSpace(fields[i])
		if f.Type == "integer" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return event{}, fmt.Errorf("%s %q is not a whole number", f.Name, v)
			}
			ev.id = n
		} else {
			ev.id = v
		}
		i++
	}
	keyText := strings.Join(fields[i:i+len(s.Keys)], "\x00")
	id, ok := p.keys[keyText]
	if !ok {
		tuple := make([]any, len(s.Keys))
		for j, k := range s.Keys {
			f := strings.TrimSpace(fields[i+j])
			if k.Type == "integer" {
				n, err := strconv.ParseInt(f, 10, 64)
				if err != nil {
					return event{}, fmt.Errorf("key %s %q is not a whole number", k.Name, f)
				}
				tuple[j] = n
			} else {
				tuple[j] = f
			}
		}
		if id, err = p.eng.Key(p.stream, tuple...); err != nil {
			return event{}, err
		}
		p.keys[keyText] = id
	}
	ev.key = id
	i += len(s.Keys)
	for j, v := range s.Values {
		x, err := strconv.ParseFloat(strings.TrimSpace(fields[i+j]), 64)
		if err != nil {
			return event{}, fmt.Errorf("value %s %q is not a number", v.Name, fields[i+j])
		}
		ev.vals = append(ev.vals, x)
	}
	return ev, nil
}

func (p *parser) parseJSON(line []byte) (event, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return event{}, fmt.Errorf("not a JSON object: %v", err)
	}
	ev := event{stream: p.stream}
	if name, ok := m["stream"].(string); ok {
		if ev.stream = p.eng.Stream(name); ev.stream < 0 {
			return event{}, fmt.Errorf("no stream %q", name)
		}
	}
	if ev.stream < 0 {
		return event{}, fmt.Errorf("the line has no stream field")
	}
	s := p.eng.Policy().Streams[ev.stream]
	num := func(name string) (float64, error) {
		v, ok := m[name]
		if !ok {
			return 0, fmt.Errorf("%s is missing", name)
		}
		n, ok := v.(json.Number)
		if !ok {
			return 0, fmt.Errorf("%s is not a number", name)
		}
		return n.Float64()
	}
	if _, ok := m["seq"]; ok {
		f, err := num("seq")
		if err != nil || f < 1 || f != math.Trunc(f) {
			return event{}, fmt.Errorf("seq must be a whole number from 1")
		}
		ev.seq = int64(f)
	}
	ts, err := num("ts")
	if err != nil || ts != math.Trunc(ts) {
		return event{}, fmt.Errorf("ts must be a whole number of seconds")
	}
	ev.ts = int64(ts)
	if f := s.ID; f != nil {
		switch x := m[f.Name].(type) {
		case string:
			ev.id = x
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return event{}, fmt.Errorf("%s must be text or a whole number", f.Name)
			}
			ev.id = n
		default:
			return event{}, fmt.Errorf("%s is missing", f.Name)
		}
	}
	tuple := make([]any, len(s.Keys))
	for j, k := range s.Keys {
		v, ok := m[k.Name]
		if !ok {
			return event{}, fmt.Errorf("key %s is missing", k.Name)
		}
		switch x := v.(type) {
		case string:
			tuple[j] = x
		case json.Number:
			n, err := x.Int64()
			if err != nil {
				return event{}, fmt.Errorf("key %s must be a whole number", k.Name)
			}
			tuple[j] = n
		default:
			return event{}, fmt.Errorf("key %s must be text or a number", k.Name)
		}
	}
	if ev.key, err = p.eng.Key(ev.stream, tuple...); err != nil {
		return event{}, err
	}
	for _, v := range s.Values {
		x, err := num(v.Name)
		if err != nil {
			return event{}, err
		}
		ev.vals = append(ev.vals, x)
	}
	return ev, nil
}
