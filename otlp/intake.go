package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/logs"
	"precomputing.com/precomputing/policy"
)

// Tables the intake keeps in the file, written with the Engine's checkpoints.
const (
	SeriesTable   = "_precomputing_otlp_series"
	RequestsTable = "_precomputing_otlp_requests"
	StateTable    = "_precomputing_otlp"
)

// RepeatWindow is how long the digest of a request is remembered, so that an exporter retrying a
// request whose answer it never got does not count it twice. OTLP exporters give up retrying after
// a few minutes.
const RepeatWindow = 3600

// SeriesExpiry is how long a cumulative series is remembered after its last point, in seconds. A
// series seen again after that sets a new baseline, as one never seen does, so it is never counted
// twice; series that come and go, such as one per pod, don't pile up.
const SeriesExpiry = 7 * 86400

// Source is the sender name OTLP events are counted under in the Engine's sources table.
const Source = "otlp"

const schema = `
CREATE TABLE IF NOT EXISTS ` + SeriesTable + ` (series TEXT PRIMARY KEY, start INTEGER NOT NULL, time INTEGER NOT NULL, a REAL NOT NULL, b REAL NOT NULL);
CREATE TABLE IF NOT EXISTS ` + RequestsTable + ` (sha256 TEXT PRIMARY KEY, received INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS ` + StateTable + ` (key TEXT PRIMARY KEY, value INTEGER NOT NULL, note TEXT NOT NULL DEFAULT '');
`

// Stats counts what the intake has done since it started.
type Stats struct {
	Requests int64 // requests read
	Repeats  int64 // requests skipped because the same request was already counted
	Records  int64 // log records read
	Points   int64 // metric points read
	Events   int64 // events given to streams
	Unused   int64 // points of metrics no stream takes
	Baseline int64 // first points of cumulative series, which set where counting starts
	Rejected int64 // records and points that could not be taken, with a reason sent back
}

// Result says what became of one request.
type Result struct {
	Events   int
	Rejected int
	Message  string // the first reason something was rejected
	Repeat   bool   // the same request was counted before: nothing was done
}

type route struct {
	stream int
	name   string
	where  []policy.Cond
	keys   []*policy.Field
	vals   []*policy.Field
	hist   bool  // every value is count, sum, min or max, so histogram points fill them by name
	since  int64 // nanoseconds: when the stream began taking this metric with this where
}

type series struct {
	key         string
	start, time int64   // nanoseconds
	a, b        float64 // the cumulative value, or a histogram's count and sum
	dirty       bool
}

// Intake takes OTLP requests into an Engine. It is not safe for concurrent use; callers
// serialize access, with the Engine's.
type Intake struct {
	eng      *engine.Engine
	red      *logs.Reducer
	byMetric map[string][]*route
	series   map[string]*series
	dirty    []*series
	seen     map[string]int64
	newSeen  []string
	stats    Stats
	pruned   int64 // when series were last expired, in seconds
	fields   map[string]string
	sb       strings.Builder
}

// New starts an intake on an Engine. Streams from logs get records when the policy has a logs
// block; streams from metrics get points. It loads what the file holds from earlier runs.
func New(eng *engine.Engine) (*Intake, error) {
	in := &Intake{eng: eng, byMetric: map[string][]*route{}, series: map[string]*series{}, seen: map[string]int64{}, fields: map[string]string{}}
	pol := eng.Policy()
	fromLogs := false
	for i, s := range pol.Streams {
		fromLogs = fromLogs || s.FromLogs
		if s.FromMetrics == "" {
			continue
		}
		rt := &route{stream: i, name: s.Name, where: s.Where, keys: s.Keys, vals: s.Values, hist: true}
		for _, v := range s.Values {
			switch v.Name {
			case "count", "sum", "min", "max":
			default:
				rt.hist = false
			}
		}
		in.byMetric[s.FromMetrics] = append(in.byMetric[s.FromMetrics], rt)
	}
	if fromLogs {
		red, err := logs.New(eng)
		if err != nil {
			return nil, err
		}
		in.red = red
	}
	st := eng.Store()
	if err := st.Exec(schema); err != nil {
		return nil, err
	}
	now := eng.Wall()
	// Each stream from metrics counts from when it began taking its metric with its where: a stream
	// added by a migration, or one whose metric or where changed, starts again from now, so it never
	// counts what happened before it took the metric.
	for _, routes := range in.byMetric {
		for _, rt := range routes {
			sig := strconv.Quote(pol.Streams[rt.stream].FromMetrics) + " where " + condText(rt.where)
			key := "since:" + rt.name
			found := false
			err := st.Query("SELECT value, note FROM "+StateTable+" WHERE key = ?", []any{key}, func(r []any) error {
				if note, _ := r[1].(string); note == sig {
					rt.since, found = asInt(r[0]), true
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if !found {
				rt.since = now * 1e9
				if err := st.Exec(fmt.Sprintf("INSERT OR REPLACE INTO %s (key, value, note) VALUES (%s, %d, %s)", StateTable, sqlQuote(key), rt.since, sqlQuote(sig))); err != nil {
					return nil, err
				}
			}
		}
	}
	err := st.Query("SELECT series, start, time, a, b FROM "+SeriesTable+" WHERE time >= ?", []any{(now - SeriesExpiry) * 1e9}, func(r []any) error {
		k, _ := r[0].(string)
		in.series[k] = &series{key: k, start: asInt(r[1]), time: asInt(r[2]), a: asReal(r[3]), b: asReal(r[4])}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = st.Query("SELECT sha256, received FROM "+RequestsTable+" WHERE received >= ?", []any{now - RepeatWindow}, func(r []any) error {
		k, _ := r[0].(string)
		in.seen[k] = asInt(r[1])
		return nil
	})
	if err != nil {
		return nil, err
	}
	eng.AddState(in)
	return in, nil
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

func asReal(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

// Stats returns the counters.
func (in *Intake) Stats() Stats { return in.stats }

// TakesLogs and TakesMetrics say whether any stream takes records or points.
func (in *Intake) TakesLogs() bool    { return in.red != nil }
func (in *Intake) TakesMetrics() bool { return len(in.byMetric) > 0 }

// repeat says whether this exact request was counted within the repeat window, and remembers it
// otherwise. signal keeps logs and metrics apart.
func (in *Intake) repeat(signal string, body []byte) bool {
	h := sha256.Sum256(append([]byte(signal+"\x00"), body...))
	k := hex.EncodeToString(h[:])
	if t, ok := in.seen[k]; ok && t >= in.eng.Wall()-RepeatWindow {
		return true
	}
	in.seen[k] = in.eng.Wall()
	in.newSeen = append(in.newSeen, k)
	return false
}

// fieldName is how an attribute is named in a policy: dots and dashes read as _.
func fieldName(k string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' {
			return '_'
		}
		return r
	}, k)
}

// fill puts attribute lists into in.fields, later lists over earlier ones.
func (in *Intake) fill(lists ...[]KV) {
	clear(in.fields)
	for _, l := range lists {
		for _, kv := range l {
			in.fields[fieldName(kv.Key)] = kv.Value
		}
	}
	if v, ok := in.fields["service_name"]; ok {
		in.fields["service"] = v
	}
}

func severity(n int64) string {
	switch {
	case n >= 1 && n <= 4:
		return "TRACE"
	case n >= 5 && n <= 8:
		return "DEBUG"
	case n >= 9 && n <= 12:
		return "INFO"
	case n >= 13 && n <= 16:
		return "WARN"
	case n >= 17 && n <= 20:
		return "ERROR"
	case n >= 21 && n <= 24:
		return "FATAL"
	}
	return ""
}

func (r *Result) reject(msg string) {
	if r.Rejected == 0 {
		r.Message = msg
	}
	r.Rejected++
}

// Logs takes the records of one request. body is the request as decoded from the wire, protobuf or
// JSON; recs are its records.
func (in *Intake) Logs(body []byte, recs []LogRecord) (Result, error) {
	var res Result
	in.stats.Requests++
	if in.repeat("logs", body) {
		in.stats.Repeats++
		return Result{Repeat: true}, nil
	}
	for i := range recs {
		r := &recs[i]
		in.stats.Records++
		if in.red == nil {
			continue
		}
		t := r.Time
		if t == 0 {
			t = r.Observed
		}
		if t == 0 || t > math.MaxInt64 {
			res.reject("a log record has no time")
			continue
		}
		ts := int64(t / 1e9)
		in.fill(r.Resource, r.Scope, r.Attrs)
		svc := in.fields["service"]
		level := r.SeverityText
		if level == "" {
			level = severity(r.SeverityNumber)
		}
		// The line kept with raw events, samples and anomalies: time, level, service and message,
		// then the record's own attributes.
		in.sb.Reset()
		in.sb.WriteString(time.Unix(0, int64(t)).UTC().Format(time.RFC3339Nano))
		for _, s := range []string{level, svc, r.Body} {
			if s != "" {
				in.sb.WriteByte(' ')
				in.sb.WriteString(s)
			}
		}
		for _, kv := range r.Attrs {
			in.sb.WriteString(" " + kv.Key + "=" + strconv.Quote(kv.Value))
		}
		out, err := in.red.PutRecord(Source, 0, ts, svc, level, r.Body, in.fields, in.sb.String())
		if err != nil {
			var stopped *engine.StoppedError
			if errors.As(err, &stopped) {
				return res, err
			}
			res.reject(err.Error())
		}
		res.Events += out.Events
		in.stats.Events += int64(out.Events)
	}
	in.stats.Rejected += int64(res.Rejected)
	return res, nil
}

// Metrics takes the points of one request.
func (in *Intake) Metrics(body []byte, ms []Metric) (Result, error) {
	var res Result
	in.stats.Requests++
	if in.repeat("metrics", body) {
		in.stats.Repeats++
		return Result{Repeat: true}, nil
	}
	for i := range ms {
		m := &ms[i]
		routes := in.byMetric[m.Name]
		in.stats.Points += int64(len(m.Points) + m.Unread)
		if len(routes) == 0 {
			in.stats.Unused += int64(len(m.Points) + m.Unread)
			continue
		}
		if m.Kind != Gauge && m.Kind != Sum && m.Kind != Histogram {
			for range m.Unread {
				res.reject(fmt.Sprintf("%s is a%s %s, which Precomputing does not take", m.Name, map[bool]string{true: "n"}[m.Kind == ExpHist], m.Kind))
			}
			continue
		}
		for j := range m.Points {
			p := &m.Points[j]
			if p.NoValue {
				continue
			}
			if p.Time == 0 || p.Time > math.MaxInt64 || p.Start > math.MaxInt64 {
				res.reject(fmt.Sprintf("%s: a point has no time", m.Name))
				continue
			}
			in.fill(m.Resource, m.Scope, p.Attrs)
			for _, rt := range routes {
				if err := in.point(rt, m, p, &res); err != nil {
					return res, err
				}
			}
		}
	}
	in.stats.Rejected += int64(res.Rejected)
	return res, nil
}

// point gives one point to one stream, when it meets the stream's where and has its keys.
func (in *Intake) point(rt *route, m *Metric, p *Point, res *Result) error {
	for _, c := range rt.where {
		if v, ok := in.fields[c.Field]; !ok || v != c.Value {
			return nil
		}
	}
	tuple := make([]any, len(rt.keys))
	for i, k := range rt.keys {
		v, ok := in.fields[k.Name]
		if !ok {
			res.reject(fmt.Sprintf("%s: a point has no attribute for key %s of stream %s", m.Name, k.Name, rt.name))
			return nil
		}
		if k.Type == "integer" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				res.reject(fmt.Sprintf("%s: %s is %q, not a whole number", m.Name, k.Name, v))
				return nil
			}
			tuple[i] = n
		} else {
			tuple[i] = v
		}
	}
	var vals []float64
	switch m.Kind {
	case Gauge, Sum:
		if len(rt.vals) != 1 {
			res.reject(fmt.Sprintf("%s is a %s, which fills one value; stream %s has %d", m.Name, m.Kind, rt.name, len(rt.vals)))
			return nil
		}
		if !p.HasValue {
			res.reject(fmt.Sprintf("%s: a point has no value", m.Name))
			return nil
		}
		v := p.Value
		if m.Kind == Sum {
			d, ok, why := in.delta(rt, m, p, []float64{p.Value})
			if !ok {
				if why != "" {
					res.reject(why)
				}
				return nil
			}
			v = d[0]
		}
		vals = []float64{v}
	case Histogram:
		if !rt.hist {
			res.reject(fmt.Sprintf("%s is a histogram; stream %s takes values named count, sum, min and max from it", m.Name, rt.name))
			return nil
		}
		count, sum := p.Count, p.Sum
		if m.Temporality == Cumulative {
			d, ok, why := in.delta(rt, m, p, []float64{p.Count, p.Sum})
			if !ok {
				if why != "" {
					res.reject(why)
				}
				return nil
			}
			count, sum = d[0], d[1]
		} else if m.Temporality != Delta {
			res.reject(fmt.Sprintf("%s: a histogram without temporality", m.Name))
			return nil
		}
		if count == 0 {
			return nil // nothing happened in the interval
		}
		for _, f := range rt.vals {
			switch f.Name {
			case "count":
				vals = append(vals, count)
			case "sum":
				if !p.HasSum {
					res.reject(fmt.Sprintf("%s: a point has no sum", m.Name))
					return nil
				}
				vals = append(vals, sum)
			case "min", "max":
				has, v := p.HasMin, p.Min
				if f.Name == "max" {
					has, v = p.HasMax, p.Max
				}
				if m.Temporality == Cumulative {
					res.reject(fmt.Sprintf("%s is cumulative: its %s covers every interval since it started, not this one", m.Name, f.Name))
					return nil
				}
				if !has {
					res.reject(fmt.Sprintf("%s: a point has no %s", m.Name, f.Name))
					return nil
				}
				vals = append(vals, v)
			}
		}
	default:
		res.reject(fmt.Sprintf("%s is a%s %s, which Precomputing does not take", m.Name, map[bool]string{true: "n"}[m.Kind == ExpHist], m.Kind))
		return nil
	}
	for i, f := range rt.vals {
		if math.IsNaN(vals[i]) || math.IsInf(vals[i], 0) {
			res.reject(fmt.Sprintf("%s: %s is not a finite number", m.Name, f.Name))
			return nil
		}
		if f.Type == "integer" && vals[i] != math.Trunc(vals[i]) {
			res.reject(fmt.Sprintf("%s: %s is %v, and stream %s keeps it as an integer", m.Name, f.Name, vals[i], rt.name))
			return nil
		}
	}
	key, err := in.eng.Key(rt.stream, tuple...)
	if err != nil {
		res.reject(err.Error())
		return nil
	}
	ev := []engine.Event{{Stream: rt.stream, TS: int64(p.Time / 1e9), Key: key, Vals: vals}}
	if err := in.eng.PutEvents(Source, 0, ev); err != nil {
		var stopped *engine.StoppedError
		if errors.As(err, &stopped) {
			return err
		}
		res.reject(err.Error())
		return nil
	}
	res.Events++
	in.stats.Events++
	return nil
}

// delta turns a cumulative point into what happened since the series' last point. A series is a
// stream, a metric and every attribute of the point, its scope and its resource. The first point
// of a series that began before the intake did only sets where counting starts; one that began
// later counts in full, as does the first point after the series restarted (a new start time, or
// a monotonic total that went down). ok is false when nothing is to be counted; why says what to
// report, or is empty when there is nothing wrong.
func (in *Intake) delta(rt *route, m *Metric, p *Point, cur []float64) ([]float64, bool, string) {
	if m.Temporality == Delta {
		return cur, true, ""
	}
	if m.Temporality != Cumulative {
		return nil, false, fmt.Sprintf("%s: a sum or histogram without temporality", m.Name)
	}
	in.sb.Reset()
	in.sb.WriteString(rt.name + "\x00" + m.Name)
	keys := make([]string, 0, len(in.fields))
	for k := range in.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		in.sb.WriteString("\x00" + k + "=" + in.fields[k])
	}
	key := in.sb.String()
	start, at := int64(p.Start), int64(p.Time)
	b := 0.0
	if len(cur) > 1 {
		b = cur[1]
	}
	s := in.series[key]
	if s == nil {
		s = &series{key: key}
		in.series[key] = s
		s.start, s.time, s.a, s.b = start, at, cur[0], b
		in.mark(s)
		// A series that began while this stream was taking the metric, and recently enough that it
		// can't be one forgotten after SeriesExpiry, holds only what happened since: count it all.
		if start != 0 && start >= rt.since && start >= (in.eng.Wall()-SeriesExpiry)*1e9 {
			return cur, true, ""
		}
		in.stats.Baseline++
		return nil, false, ""
	}
	if at <= s.time {
		return nil, false, "" // a point the series already moved past, such as a repeat
	}
	monotonic := m.Kind == Histogram || m.Monotonic
	var out []float64
	if start != s.start || (monotonic && cur[0] < s.a) {
		out = cur // the series restarted: everything it holds happened since
	} else {
		out = []float64{cur[0] - s.a}
		if len(cur) > 1 {
			out = append(out, cur[1]-s.b)
		}
	}
	s.start, s.time, s.a, s.b = start, at, cur[0], b
	in.mark(s)
	return out, true, ""
}

func (in *Intake) mark(s *series) {
	if !s.dirty {
		s.dirty = true
		in.dirty = append(in.dirty, s)
	}
}

// Blocks writes the series and request digests that changed, and forgets digests older than the
// repeat window.
func (in *Intake) Blocks(bt *engine.Batch) {
	if b := in.expire(); b != nil {
		bt.Blocks = append(bt.Blocks, b)
	}
	if len(in.dirty) > 0 {
		b := engine.NewBlock("INSERT OR REPLACE INTO "+SeriesTable+" (series, start, time, a, b) VALUES (?, ?, ?, ?, ?)", "tiiff")
		for _, s := range in.dirty {
			b.AddText(s.key)
			b.AddInt(s.start)
			b.AddInt(s.time)
			b.AddReal(s.a)
			b.AddReal(s.b)
			b.EndRow()
		}
		bt.Blocks = append(bt.Blocks, b)
	}
	if len(in.newSeen) > 0 {
		b := engine.NewBlock("INSERT OR REPLACE INTO "+RequestsTable+" (sha256, received) VALUES (?, ?)", "ti")
		for _, k := range in.newSeen {
			b.AddText(k)
			b.AddInt(in.seen[k])
			b.EndRow()
		}
		bt.Blocks = append(bt.Blocks, b)
		old := in.eng.Wall() - RepeatWindow
		d := engine.NewBlock("DELETE FROM "+RequestsTable+" WHERE received < ?", "i")
		d.AddInt(old)
		d.EndRow()
		bt.Blocks = append(bt.Blocks, d)
	}
}

// expire forgets series not seen for SeriesExpiry, at most once an hour, and returns the block that
// deletes them from the file, or nil.
func (in *Intake) expire() *engine.Block {
	now := in.eng.Wall()
	if now-in.pruned < 3600 {
		return nil
	}
	in.pruned = now
	cut := (now - SeriesExpiry) * 1e9
	for k, s := range in.series {
		if s.time < cut && !s.dirty {
			delete(in.series, k)
		}
	}
	b := engine.NewBlock("DELETE FROM "+SeriesTable+" WHERE time < ?", "i")
	b.AddInt(cut)
	b.EndRow()
	return b
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func condText(cs []policy.Cond) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Field+"="+strconv.Quote(c.Value))
	}
	return strings.Join(parts, " and ")
}

// Written is called once a checkpoint is in the file.
func (in *Intake) Written() {
	for _, s := range in.dirty {
		s.dirty = false
	}
	in.dirty = in.dirty[:0]
	if len(in.newSeen) > 0 {
		old := in.eng.Wall() - RepeatWindow
		for k, t := range in.seen {
			if t < old {
				delete(in.seen, k)
			}
		}
	}
	in.newSeen = in.newSeen[:0]
}
