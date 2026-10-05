// Package logs turns log lines into events for a policy's streams from logs. It reads each line
// with the policy's format, learns the line's template with Drain, and hands the line to every
// stream whose where it meets, with keys and values read from the line's fields.
//
// The templates live in the Engine's file and are written with its checkpoints, in the same
// transaction as the rows. After a crash the reducer rebuilds its template tree from the file, and
// lines sent again from the last checkpoint go exactly where they would have gone.
package logs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/internal/drain"
	"precomputing.com/precomputing/policy"
)

// Template is a log template the reducer has learned.
type Template struct {
	ID      int64
	Service string
	Level   string
	FirstTS int64
	LastTS  int64
	Example string // the first line with this template
	cluster *drain.Cluster
	dirty   bool
}

// Text is the template with <*> where the lines differ.
func (t *Template) Text() string { return strings.Join(t.cluster.Template, " ") }

// Count is the number of lines with this template.
func (t *Template) Count() int64 { return t.cluster.N }

// Stats counts what a reducer has done since it started.
type Stats struct {
	Lines        int64 // lines given to Put, repeats included
	NotRead      int64 // lines that did not match the format or had no readable time
	Events       int64 // events handed to streams
	NewTemplates int64
}

// Result says what became of one line.
type Result struct {
	Read     bool      // the line matched the format and had a readable time
	Repeat   bool      // its sequence number was already applied: nothing was done
	TS       int64     // the line's time, in seconds
	Template *Template // its template
	New      bool      // the template was new with this line
	Level    string    // the line's level field, if the format has one
	Events   int       // streams the line was given to
}

type field struct {
	name    string
	integer bool
}

type route struct {
	stream int
	where  []policy.Cond
	keys   []field
	vals   []field
	cache  map[string]int32
	tuple  []any
	sb     strings.Builder
}

// Reducer reads log lines into an Engine.
type Reducer struct {
	eng       *engine.Engine
	lg        *policy.Logs
	format    *regexp.Regexp
	names     []string // submatch index -> field name
	msg       int
	ts        int
	date, tm  int
	svc       int
	level     int
	kv        *regexp.Regexp
	masks     []*regexp.Regexp
	miners    map[string]*drain.Parser
	byCluster map[*drain.Cluster]*Template
	templates []*Template
	dirty     []*Template
	routes    []*route
	fields    map[string]string
	evs       []engine.Event
	lastStamp string
	lastSecs  int64
	stats     Stats
	simple    int      // fields in a format of fields separated by single spaces; 0 otherwise
	parts     []string // reused by the fast path
	sb        strings.Builder
}

// kvRE finds key=value pairs in a message; a value in double quotes may hold spaces.
var kvRE = regexp.MustCompile(`(^|\s)([A-Za-z_][A-Za-z0-9_.]*)=("[^"]*"|\S*)`)

var simpleFormat = regexp.MustCompile(`^<[a-z][a-z0-9_]*>( <[a-z][a-z0-9_]*>)*$`)

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

// split reads a line of a simple format into its fields: parts[1..n], as the format's regular
// expression would, or reports that the line does not match.
func (r *Reducer) split(line string) bool {
	n := r.simple
	i := 0
	for f := 1; f < n; f++ {
		start := i
		for i < len(line) && !isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return false
		}
		r.parts[f] = line[start:i]
		for i < len(line) && isSpace(line[i]) {
			i++
		}
	}
	r.parts[n] = line[i:]
	return true
}

func isKeyStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isKeyChar(c byte) bool  { return isKeyStart(c) || (c >= '0' && c <= '9') || c == '.' }

// maskPairs finds key=value pairs as kvRE does, adds them to fields (the first of a name wins,
// after the format's own fields), and returns the message with each value replaced by <*>.
func (r *Reducer) maskPairs(msg string) string {
	b := &r.sb
	b.Reset()
	last := 0
	i := 0
	for i < len(msg) {
		// A key starts the message or follows white space.
		if (i > 0 && !isSpace(msg[i-1])) || !isKeyStart(msg[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(msg) && isKeyChar(msg[j]) {
			j++
		}
		if j >= len(msg) || msg[j] != '=' {
			i = j
			continue
		}
		k := msg[i:j]
		v0 := j + 1
		v1 := v0
		quoted := false
		if v0 < len(msg) && msg[v0] == '"' {
			if q := strings.IndexByte(msg[v0+1:], '"'); q >= 0 {
				v1, quoted = v0+1+q+1, true
			}
		}
		if !quoted {
			for v1 < len(msg) && !isSpace(msg[v1]) {
				v1++
			}
		}
		v := msg[v0:v1]
		if quoted {
			v = v[1 : len(v)-1]
		}
		if _, taken := r.fields[k]; !taken {
			r.fields[k] = v
		}
		b.WriteString(msg[last:v0])
		b.WriteString(drain.Wildcard)
		last = v1
		i = v1
	}
	if last == 0 {
		return msg
	}
	b.WriteString(msg[last:])
	return b.String()
}

// New starts a reducer on an Engine whose policy has a logs block, and loads the templates its
// file holds. The reducer writes its templates with the Engine's checkpoints.
func New(eng *engine.Engine) (*Reducer, error) {
	pol := eng.Policy()
	if pol.Logs == nil {
		return nil, fmt.Errorf("the policy has no logs block")
	}
	re, names, err := policy.LogFormat(pol.Logs.Format)
	if err != nil {
		return nil, err
	}
	r := &Reducer{eng: eng, lg: pol.Logs, format: re, kv: kvRE, miners: map[string]*drain.Parser{},
		byCluster: map[*drain.Cluster]*Template{}, fields: map[string]string{}}
	r.names = re.SubexpNames()
	idx := func(name string) int { return re.SubexpIndex(name) }
	r.msg, r.ts, r.date, r.tm, r.svc, r.level = idx("message"), idx("timestamp"), idx("date"), idx("time"), idx("service"), idx("level")
	_ = names
	for _, m := range pol.Logs.Masks {
		r.masks = append(r.masks, regexp.MustCompile(m))
	}
	// A format of fields separated by single spaces, ending with the message, is read by splitting
	// at the first runs of white space, which is what its regular expression matches, only faster.
	if simpleFormat.MatchString(pol.Logs.Format) && names[len(names)-1] == "message" {
		r.simple = len(names)
		r.parts = make([]string, len(names)+1)
	}
	for i, s := range pol.Streams {
		if !s.FromLogs {
			continue
		}
		rt := &route{stream: i, where: s.Where, cache: map[string]int32{}}
		for _, k := range s.Keys {
			rt.keys = append(rt.keys, field{k.Name, k.Type == "integer"})
		}
		for _, v := range s.Values {
			rt.vals = append(rt.vals, field{v.Name, v.Type == "integer"})
		}
		r.routes = append(r.routes, rt)
	}
	err = eng.Store().Query("SELECT id, service, level, template, initial, n, first_ts, last_ts, example FROM "+compile.TemplatesTable+" ORDER BY id", nil, func(row []any) error {
		id := asInt(row[0])
		if id != int64(len(r.templates))+1 {
			return fmt.Errorf("the templates in the file are not numbered 1, 2, 3...")
		}
		svc, _ := row[1].(string)
		level, _ := row[2].(string)
		tmpl, _ := row[3].(string)
		initial, _ := row[4].(string)
		n, first, last := asInt(row[5]), asInt(row[6]), asInt(row[7])
		example, _ := row[8].(string)
		c := r.miner(svc, level).Restore(strings.Fields(initial), strings.Fields(tmpl), n)
		t := &Template{ID: id, Service: svc, Level: level, FirstTS: first, LastTS: last, Example: example, cluster: c}
		r.templates = append(r.templates, t)
		r.byCluster[c] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	eng.AddState(r)
	return r, nil
}

// asInt reads an integer column. Native SQLite gives an int64; the browser's store gives every
// number as a float64, exact below 2^53.
func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

// miner is the template tree of one service and level: lines of different services or levels
// never share a template.
func (r *Reducer) miner(service, level string) *drain.Parser {
	k := service + "\x00" + level
	m := r.miners[k]
	if m == nil {
		m = drain.New(drain.Options{Depth: r.lg.Depth, Similarity: r.lg.Similarity})
		r.miners[k] = m
	}
	return m
}

// Templates returns every template learned, numbered from 1.
func (r *Reducer) Templates() []*Template { return r.templates }

// Stats returns counters since the reducer started.
func (r *Reducer) Stats() Stats { return r.stats }

// LastEvents returns the events the last line gave the streams. They are valid until the next Put.
func (r *Reducer) LastEvents() []engine.Event { return r.evs }

// Put reads one line. seq is its sequence number from src, counted from 1 (the line number, for a
// file); a line whose number was already applied is skipped, so a sender that resends from the
// last acknowledged number after a crash loses nothing and counts nothing twice.
func (r *Reducer) Put(src string, seq int64, line string) (Result, error) {
	r.stats.Lines++
	r.evs = r.evs[:0]
	if seq > 0 && seq <= r.eng.Applied(src) {
		return Result{Repeat: true}, r.eng.PutEvents(src, seq, nil)
	}
	text := strings.TrimRight(line, "\r\n")
	var m []string
	if r.simple > 0 {
		if r.split(strings.TrimSpace(text)) {
			m = r.parts
		}
	} else {
		m = r.format.FindStringSubmatch(strings.TrimSpace(text))
	}
	if m == nil {
		r.stats.NotRead++
		return Result{}, r.eng.PutEvents(src, seq, nil)
	}
	ts, ok := r.time(m)
	if !ok {
		r.stats.NotRead++
		return Result{}, r.eng.PutEvents(src, seq, nil)
	}
	clear(r.fields)
	for i, name := range r.names {
		if i > 0 && name != "" && i != r.msg {
			r.fields[name] = m[i]
		}
	}
	// Values in key=value pairs become fields, and are masked before the template is learned.
	msg := r.maskPairs(m[r.msg])
	for _, mk := range r.masks {
		msg = mk.ReplaceAllLiteralString(msg, drain.Wildcard)
	}
	svc, level := "", ""
	if r.svc > 0 {
		svc = m[r.svc]
	}
	if r.level > 0 {
		level = m[r.level]
	}
	c, created := r.miner(svc, level).Add(strings.Fields(msg))
	t := r.byCluster[c]
	if created {
		t = &Template{ID: int64(len(r.templates)) + 1, Service: svc, Level: level, FirstTS: ts, Example: text, cluster: c}
		r.templates = append(r.templates, t)
		r.byCluster[c] = t
		r.stats.NewTemplates++
	}
	if ts > t.LastTS {
		t.LastTS = ts
	}
	if !t.dirty {
		t.dirty = true
		r.dirty = append(r.dirty, t)
	}
	r.fields["template"] = strconv.FormatInt(t.ID, 10)

	evs := r.evs[:0]
	for _, rt := range r.routes {
		if ev, ok, err := rt.event(r, ts, text); err != nil {
			return Result{}, err
		} else if ok {
			evs = append(evs, ev)
		}
	}
	r.evs = evs
	r.stats.Events += int64(len(evs))
	res := Result{Read: true, TS: ts, Template: t, New: created, Level: level, Events: len(evs)}
	return res, r.eng.PutEvents(src, seq, evs)
}

// event builds the stream's event from the line's fields, when the line meets the stream's where
// and has every key and value the stream needs.
func (rt *route) event(r *Reducer, ts int64, line string) (engine.Event, bool, error) {
	for _, c := range rt.where {
		if v, ok := r.fields[c.Field]; !ok || v != c.Value {
			return engine.Event{}, false, nil
		}
	}
	rt.sb.Reset()
	for i, k := range rt.keys {
		v, ok := r.fields[k.name]
		if !ok {
			return engine.Event{}, false, nil
		}
		if i > 0 {
			rt.sb.WriteByte(0)
		}
		rt.sb.WriteString(v)
	}
	ck := rt.sb.String()
	key, ok := rt.cache[ck]
	if !ok {
		rt.tuple = rt.tuple[:0]
		for _, k := range rt.keys {
			v := r.fields[k.name]
			if k.integer {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					return engine.Event{}, false, nil
				}
				rt.tuple = append(rt.tuple, n)
			} else {
				rt.tuple = append(rt.tuple, v)
			}
		}
		var err error
		if key, err = r.eng.Key(rt.stream, rt.tuple...); err != nil {
			return engine.Event{}, false, err
		}
		rt.cache[ck] = key
	}
	var vals []float64
	if len(rt.vals) > 0 {
		vals = make([]float64, len(rt.vals))
		for i, f := range rt.vals {
			v, ok := r.fields[f.name]
			if !ok {
				return engine.Event{}, false, nil
			}
			x, err := strconv.ParseFloat(v, 64)
			if err != nil || (f.integer && x != float64(int64(x))) {
				return engine.Event{}, false, nil
			}
			vals[i] = x
		}
	}
	return engine.Event{Stream: rt.stream, TS: ts, Key: key, Vals: vals, Line: line}, true, nil
}

// time reads a line's time in whole seconds: an RFC 3339 timestamp, a date and time with a space
// or a T between them (UTC unless a zone is given), or seconds since 1970.
func (r *Reducer) time(m []string) (int64, bool) {
	var stamp string
	switch {
	case r.ts > 0:
		stamp = m[r.ts]
	case r.date > 0 && r.tm > 0:
		stamp = m[r.date] + "T" + m[r.tm]
	default:
		return 0, false
	}
	// Lines of the same second share their first 19 characters: read those once.
	if len(stamp) >= 19 && len(r.lastStamp) >= 19 && stamp[:19] == r.lastStamp[:19] && zoneless(stamp) && zoneless(r.lastStamp) {
		return r.lastSecs, true
	}
	secs, ok := parseTime(stamp)
	if ok {
		r.lastStamp, r.lastSecs = stamp, secs
	}
	return secs, ok
}

// zoneless reports whether a timestamp ends without a zone offset or with Z, so that its first 19
// characters decide its second.
func zoneless(s string) bool {
	if len(s) <= 19 {
		return true
	}
	tail := s[19:]
	return !strings.ContainsAny(tail, "+-")
}

var layouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05,999999999",
	"2006-01-02 15:04:05,999999999",
}

func parseTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if c := s[0]; c >= '0' && c <= '9' && !strings.ContainsAny(s, "-:T ") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return int64(f), true
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// Blocks writes the templates that changed since the last checkpoint.
func (r *Reducer) Blocks(bt *engine.Batch) {
	if len(r.dirty) == 0 {
		return
	}
	b := engine.NewBlock("INSERT OR REPLACE INTO "+compile.TemplatesTable+" (id, service, level, template, initial, n, first_ts, last_ts, example) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", "ittttiiit")
	for _, t := range r.dirty {
		b.AddInt(t.ID)
		b.AddText(t.Service)
		b.AddText(t.Level)
		b.AddText(t.Text())
		b.AddText(strings.Join(t.cluster.Initial, " "))
		b.AddInt(t.cluster.N)
		b.AddInt(t.FirstTS)
		b.AddInt(t.LastTS)
		b.AddText(t.Example)
		b.EndRow()
	}
	bt.Blocks = append(bt.Blocks, b)
}

// Written is called once a checkpoint is in the file.
func (r *Reducer) Written() {
	for _, t := range r.dirty {
		t.dirty = false
	}
	r.dirty = r.dirty[:0]
}
