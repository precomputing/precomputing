package policy

import (
	"strings"
	"testing"
)

const good = `
stream latency {
  key endpoint text
  value ms real
  derive seconds = ms / 1000
  raw keep 10m
  rollup 1h keep 1y quantiles ms
  rollup 10s keep 24h
  rollup 1m keep 30d quantiles ms, seconds
  samples 3 per 1m
  anomalies ms log z > 4 keep 20 per 1m
  quantile accuracy 2%
}
precompute avg_ms = avg(latency.ms) by endpoint
precompute p999 = p999(latency.seconds) by endpoint per day
precompute total = count(latency)
`

func TestParseGood(t *testing.T) {
	p, err := Parse(good)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Streams[0]
	if len(s.Keys) != 1 || len(s.Values) != 1 || len(s.Derived) != 1 {
		t.Fatalf("fields: %+v", s)
	}
	if s.RawKeep != 600 {
		t.Errorf("raw keep = %d", s.RawKeep)
	}
	if got := []int64{s.Rollups[0].Res, s.Rollups[1].Res, s.Rollups[2].Res}; got[0] != 10 || got[1] != 60 || got[2] != 3600 {
		t.Errorf("rollups not sorted: %v", got)
	}
	if s.Accuracy != 0.02 {
		t.Errorf("accuracy = %v", s.Accuracy)
	}
	if a := s.Anomalies; a == nil || !a.Log || a.Z != 4 || a.Keep != 20 || a.Per != 60 || a.Memory != 3000 || a.Warmup != 500 {
		t.Errorf("anomalies = %+v", a)
	}
	pc := p.Precomputes[1]
	if pc.Func != "quantile" || pc.Q != 0.999 || pc.Per != "day" || pc.Field != "seconds" {
		t.Errorf("p999 = %+v", pc)
	}
	if p.Precomputes[2].Func != "count" || p.Precomputes[2].Field != "" {
		t.Errorf("count = %+v", p.Precomputes[2])
	}
}

func TestDefaults(t *testing.T) {
	p, err := Parse("stream s { value v rollup 10s keep 1d rollup 5m keep 7d anomalies v z > 3 }")
	if err != nil {
		t.Fatal(err)
	}
	s := p.Streams[0]
	if s.Accuracy != DefaultAccuracy {
		t.Errorf("accuracy = %v", s.Accuracy)
	}
	if s.Anomalies.Per != 300 {
		t.Errorf("anomaly window = %d, want the first rollup of at least a minute", s.Anomalies.Per)
	}
	if s.Values[0].Type != "real" {
		t.Errorf("value type = %q", s.Values[0].Type)
	}
}

func TestExact(t *testing.T) {
	src := `stream usage exact {
  id     request_id refuse repeats 7d
  key    customer text
  value  tokens integer
  late   24h
  period month close 24h
  raw    until closed + 90d
  rollup 1d keep forever
}
precompute tokens_month = sum(usage.tokens) by customer per month
quota monthly_tokens = tokens_month`
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Streams[0]
	if !s.Exact || s.ID.Name != "request_id" || s.ID.Type != "text" || s.Repeats != 7*86400 || s.Late != 86400 ||
		s.Period != "month" || s.Close != 86400 || !s.RawUntilClosed || s.RawAfterClose != 90*86400 {
		t.Errorf("stream = %+v", s)
	}
	if len(p.Quotas) != 1 || p.Quotas[0].Name != "monthly_tokens" || p.Quotas[0].Precompute != "tokens_month" {
		t.Errorf("quotas = %+v", p.Quotas)
	}
}

func TestChange(t *testing.T) {
	for _, src := range []string{
		"stream s { key k value p rollup 1m keep 1d anomalies p log change z > 6 }",
		"stream s { key k value p rollup 1m keep 1d anomalies p change log z > 6 }",
	} {
		p, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if a := p.Streams[0].Anomalies; !a.Log || !a.Change || a.Z != 6 {
			t.Errorf("%q: anomalies = %+v", src, a)
		}
	}
	p, err := Parse("stream s { value p rollup 1m keep 1d anomalies p change z > 6 }")
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Streams[0].Anomalies; a.Log || !a.Change {
		t.Errorf("anomalies = %+v", a)
	}
}

func TestLogs(t *testing.T) {
	p, err := Parse(`logs {
  format "<timestamp> <level> \[<service>\] <message>"
  mask   "[0-9a-f]{8}-[0-9a-f-]{27}"
  mask   "say \"hi\""
  templates depth 5 similarity 0.6
}
stream web from logs where service = "web" and level = "INFO" {
  key   route text
  key   template integer
  value ms real
  rollup 1m keep 1d
}
stream lines from logs { key service rollup 1m keep 1d }`)
	if err != nil {
		t.Fatal(err)
	}
	lg := p.Logs
	if lg.Format != `<timestamp> <level> \[<service>\] <message>` || len(lg.Masks) != 2 || lg.Masks[1] != `say "hi"` || lg.Depth != 5 || lg.Similarity != 0.6 {
		t.Errorf("logs = %+v", lg)
	}
	if strings.Join(lg.Fields, ",") != "timestamp,level,service,message" {
		t.Errorf("fields = %v", lg.Fields)
	}
	s := p.Streams[0]
	if !s.FromLogs || len(s.Where) != 2 || s.Where[1] != (Cond{Field: "level", Value: "INFO", Pos: s.Where[1].Pos}) {
		t.Errorf("stream = %+v", s)
	}
	re, _, err := LogFormat(lg.Format)
	if err != nil {
		t.Fatal(err)
	}
	m := re.FindStringSubmatch("2026-09-29T10:00:00Z  INFO [web] GET / route=/ ms=12")
	if m == nil || m[re.SubexpIndex("service")] != "web" || m[re.SubexpIndex("message")] != "GET / route=/ ms=12" {
		t.Errorf("match = %q", m)
	}
	if p, err := Parse(`logs { format "<timestamp> <message>" }`); err != nil || p.Logs.Depth != DefaultDepth || p.Logs.Similarity != DefaultSimilarity {
		t.Errorf("defaults: %+v %v", p.Logs, err)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct{ src, pos, msg string }{
		{"stream s { value v", "1:19", "expected a stream line"},
		{"table s {}", "1:1", `expected "stream", "precompute", "quota" or "logs"`},
		{"stream s { id r refuse repeats 7d value v }", "1:15", "id belongs in an exact stream"},
		{"stream s exact { value v raw until closed }", "1:10", "needs a period"},
		{"stream s exact { value v period month close 1d }", "1:10", "raw until closed"},
		{"stream s exact { value v period day close 1h raw until closed rollup 1h keep 1d quantiles v }", "1:63", "quantile sketches are approximate"},
		{"stream s exact { key k value v period day close 1h raw until closed } precompute a = avg(s.v) by k per day quota q = a", "1:108", "limits a sum or a count"},
		{"stream s exact { key k value v period day close 1h raw until closed } precompute a = sum(s.v) by k quota q = a", "1:100", "per month"},
		{"stream s exact { value v period week close 1h }", "1:33", "hour, day or month"},
		{"stream s { key k }", "1:1", "needs at least one value"},
		{"stream s { value v rollup 10x keep 1d }", "1:27", "not a number or a duration"},
		{"stream s { value v rollup 1m keep 10s }", "1:20", "less than one window"},
		{"stream s { value v rollup 1m keep 1d rollup 1m keep 2d }", "1:38", "already declared"},
		{"stream s { value select }", "1:18", "reserved word"},
		{"stream s { value Ms }", "1:18", "must start with a lowercase letter"},
		{"stream s { value v value v }", "1:26", "already declared"},
		{"stream s { key k real value v }", "1:18", "real numbers belong in a value"},
		{"stream s { value v derive d = v * k }", "1:35", "not a value declared above"},
		{"stream s { value v samples 3 per 1m }", "1:20", "needs a rollup 1m"},
		{"stream s { value v anomalies v z > 3 }", "1:20", "at least one rollup"},
		{"stream s { value v rollup 1m keep 1d anomalies v log change log z > 3 }", "1:61", `"log" is written twice`},
		{"stream s { value v rollup 1m keep 1d quantiles w }", "1:20", "has no value"},
		{"stream s { value v quantile accuracy 60% }", "1:38", "below 50%"},
		{"precompute x = avg(s.v)", "1:1", "not declared"},
		{"stream s { value v } precompute x = median(s.v)", "1:37", "not a function"},
		{"stream s { key k value v } precompute x = avg(s.k)", "1:28", "is a key of stream"},
		{"stream s { value v } precompute x = avg(s)", "1:22", "needs a value"},
		{"stream s { value v } precompute x = count(s) by v", "1:22", "is not a key"},
		{"stream s { value v } precompute s = count(s)", "1:22", "same name as a stream"},
		{"stream s { value v } precompute x = count(s) per week", "1:46", "per takes hour, day or month"},
		{`stream s from logs { key service }`, "1:10", "needs a logs block"},
		{`logs { mask "a" }`, "1:1", "needs a format"},
		{`logs { format "<timestamp> <level>" }`, "1:15", "needs a <message> field"},
		{`logs { format "<level> <message>" }`, "1:15", "the time of each line"},
		{`logs { format "<timestamp> <message>" mask "a(" }`, "1:44", "not a regular expression"},
		{`logs { format "<timestamp> <message> }`, "1:15", "not closed"},
		{`logs { format "<timestamp> <message>" templates similarity 1.5 }`, "1:60", "at most 1"},
		{`logs { format "<timestamp> <message>" } stream s from logs { key template }`, "1:66", "key template integer"},
		{`logs { format "<timestamp> <message>" } stream s from logs { key line }`, "1:66", "the log line itself"},
		{`logs { format "<timestamp> <message>" } stream s from logs where service { key k }`, "1:74", "expected '='"},
		{`logs { format "<timestamp> <message>" } stream s from logs where service = web { key k }`, "1:76", "double quotes"},
	}
	for _, c := range cases {
		_, err := Parse(c.src)
		if err == nil {
			t.Errorf("%q: no error, want %q", c.src, c.msg)
			continue
		}
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%q: error %T %v", c.src, err, err)
			continue
		}
		if e.Pos.String() != c.pos || !strings.Contains(e.Msg, c.msg) {
			t.Errorf("%q:\n  got  %s: %s\n  want %s: ...%s...", c.src, e.Pos, e.Msg, c.pos, c.msg)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for secs, want := range map[int64]string{10: "10s", 60: "1m", 90: "90s", 3600: "1h", 86400: "1d", 604800: "1w", 31536000: "1y", -1: "forever"} {
		if got := FormatDuration(secs); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", secs, got, want)
		}
	}
}
