//go:build cgo

package otlp

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/policy"
)

const T = 1790586000

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The fixtures come from the official protobuf classes (testdata/generate.py). Both encodings of a
// request must decode to the same records and points.
func TestDecodeLogs(t *testing.T) {
	pb, err := DecodeLogs(fixture(t, "logs.pb"))
	if err != nil {
		t.Fatal(err)
	}
	js, err := DecodeLogsJSON(fixture(t, "logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pb, js) {
		t.Fatalf("protobuf and JSON differ:\n%+v\n%+v", pb, js)
	}
	if len(pb) != 4 {
		t.Fatalf("%d records, want 4", len(pb))
	}
	r := pb[0]
	if r.Time != T*1e9+5e8 || r.SeverityNumber != 17 || r.Body != "payment failed order=81 ms=120" {
		t.Errorf("first record: %+v", r)
	}
	if !reflect.DeepEqual(r.Resource, []KV{{"service.name", "checkout"}, {"host.name", "h1"}}) ||
		!reflect.DeepEqual(r.Scope, []KV{{"otel.scope.name", "shop.payments"}, {"otel.scope.version", "1.2"}}) ||
		!reflect.DeepEqual(r.Attrs, []KV{{"http.route", "/pay"}, {"user.id", "42"}}) {
		t.Errorf("first record's attributes: %+v", r)
	}
	if pb[1].Time != 0 || pb[1].Observed != (T+1)*1e9 {
		t.Errorf("second record's times: %d %d", pb[1].Time, pb[1].Observed)
	}
	if got := pb[1].Attrs[1:]; !reflect.DeepEqual(got, []KV{{"cart", `["a","2","true"]`}, {"extra", `{"k":"1.5"}`}}) {
		t.Errorf("an array and a map as attribute values: %+v", got)
	}
	if got := pb[3].Attrs[1]; got != (KV{"raw", "AAE="}) {
		t.Errorf("bytes as an attribute value: %+v", got)
	}
}

func TestDecodeMetrics(t *testing.T) {
	for _, name := range []string{"metrics-1", "metrics-2", "metrics-3"} {
		pb, err := DecodeMetrics(fixture(t, name+".pb"))
		if err != nil {
			t.Fatal(err)
		}
		js, err := DecodeMetricsJSON(fixture(t, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pb, js) {
			t.Fatalf("%s: protobuf and JSON differ:\n%+v\n%+v", name, pb, js)
		}
	}
	ms, _ := DecodeMetrics(fixture(t, "metrics-1.pb"))
	kinds := []string{}
	for _, m := range ms {
		kinds = append(kinds, m.Name+":"+m.Kind)
	}
	want := "queue.depth:gauge http.requests:sum http.server.request.duration:histogram rpc.duration:summary db.duration:exponential histogram not.taken:gauge"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("metrics: %v", kinds)
	}
	if p := ms[0].Points[0]; p.Value != 5 || !p.HasValue || p.Time != T*1e9 {
		t.Errorf("an integer gauge point: %+v", p)
	}
	if s := ms[1]; s.Temporality != Cumulative || !s.Monotonic || s.Points[0].Value != 10 || s.Points[0].Start != (T-600)*1e9 {
		t.Errorf("a cumulative sum: %+v", s)
	}
	if h := ms[2].Points[0]; ms[2].Temporality != Delta || h.Count != 4 || h.Sum != 0.5 || h.Min != 0.05 || h.Max != 0.2 || !h.HasMin || !h.HasMax {
		t.Errorf("a histogram point: %+v", h)
	}
}

func TestDecodeDamaged(t *testing.T) {
	good := fixture(t, "metrics-1.pb")
	for n := 1; n < len(good); n += 7 {
		if _, err := DecodeMetrics(good[:n]); err == nil && n < len(good)-1 {
			// A cut can land between whole fields and still decode; it must never panic.
			continue
		}
	}
	if _, err := DecodeLogs([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f}); err == nil {
		t.Error("a length beyond the message was accepted")
	}
	if _, err := DecodeLogsJSON([]byte(`{"resourceLogs": [] } {}`)); err == nil {
		t.Error("two JSON values were accepted")
	}
	// Values nested deeper than maxDepth are refused, in both encodings.
	deep := `{"stringValue":"x"}`
	for i := 0; i < maxDepth+2; i++ {
		deep = `{"arrayValue":{"values":[` + deep + `]}}`
	}
	if _, err := DecodeLogsJSON([]byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":` + deep + `}]}]}]}`)); err == nil {
		t.Error("a value nested too deeply was accepted")
	}
}

const testPolicy = `
logs {
  mask "[0-9]+"
}

stream errors from logs where level = "ERROR" {
  key   service text
  key   http_route text
  value ms real
  raw   keep 1d
  rollup 1m keep 1d
}

stream queue from metrics "queue.depth" {
  key   queue text
  value depth real
  rollup 1m keep 1d
}

stream requests from metrics "http.requests" where service = "api" {
  key   http_route text
  value calls integer
  rollup 1m keep 1d
}

stream latency from metrics "http.server.request.duration" {
  key   http_route text
  value count integer
  value sum real
  value max real
  rollup 1m keep 1d
}

precompute errors_by_service = count(errors) by service
precompute requests_total = sum(requests.calls) by http_route
precompute latency_count = sum(latency.count) by http_route
precompute latency_max = max(latency.max) by http_route
`

func open(t *testing.T, src string, wall int64) (*engine.Engine, *sqlitestore.Store, *Intake) {
	t.Helper()
	pol, err := policy.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "f.db"), sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng, err := engine.Open(st, pol, "test.precompute")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetWall(func() int64 { return wall })
	in, err := New(eng)
	if err != nil {
		t.Fatal(err)
	}
	return eng, st, in
}

func rows(t *testing.T, st *sqlitestore.Store, sql string) string {
	t.Helper()
	var out []string
	err := st.Query(sql, nil, func(r []any) error {
		var parts []string
		for _, v := range r {
			parts = append(parts, toString(v))
		}
		out = append(out, strings.Join(parts, " "))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "; ")
}

func toString(v any) string { return fmt.Sprint(v) }

func TestIntakeLogs(t *testing.T) {
	for _, enc := range []string{"pb", "json"} {
		t.Run(enc, func(t *testing.T) {
			eng, st, in := open(t, testPolicy, T+10)
			body := fixture(t, "logs."+enc)
			recs, err := DecodeLogs(body)
			if enc == "json" {
				recs, err = DecodeLogsJSON(body)
			}
			if err != nil {
				t.Fatal(err)
			}
			res, err := in.Logs(body, recs)
			if err != nil {
				t.Fatal(err)
			}
			if res.Events != 2 || res.Rejected != 1 || !strings.Contains(res.Message, "no time") {
				t.Fatalf("result %+v: want 2 error events and the record without a time rejected", res)
			}
			if err := eng.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			if got := rows(t, st, "SELECT service, value FROM errors_by_service ORDER BY service"); got != "checkout 1; web 1" {
				t.Errorf("errors by service: %s", got)
			}
			if got := rows(t, st, "SELECT ts, service, http_route, ms, line FROM errors_raw ORDER BY ts"); got !=
				`1790586000 checkout /pay 120 2026-09-28T09:00:00.5Z ERROR checkout payment failed order=81 ms=120 http.route="/pay" user.id="42"; `+
					`1790586002 web /search 3000 2026-09-28T09:00:02Z ERROR web upstream timeout ms=3000 http.route="/search" raw="AAE="` {
				t.Errorf("raw errors: %s", got)
			}
			if got := rows(t, st, "SELECT service, level, template FROM _precomputing_templates ORDER BY id"); got !=
				"checkout ERROR payment failed order=<*> ms=<*>; checkout INFO payment ok order=<*> ms=<*>; web ERROR upstream timeout ms=<*>" {
				t.Errorf("templates: %s", got)
			}
			// The exporter sends the same request again: it is counted once.
			again, err := in.Logs(body, recs)
			if err != nil || !again.Repeat {
				t.Fatalf("a repeated request: %+v %v", again, err)
			}
		})
	}
}

func TestIntakeMetrics(t *testing.T) {
	// The series of http.requests started at T-600, before the intake did (T-300): its first point
	// only sets the baseline.
	eng, st, in := open(t, testPolicy, T-300)
	send := func(name string) Result {
		t.Helper()
		body := fixture(t, name+".pb")
		ms, err := DecodeMetrics(body)
		if err != nil {
			t.Fatal(err)
		}
		res, err := in.Metrics(body, ms)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		return res
	}
	r1 := send("metrics-1")
	// gauge a and b and the histogram; the cumulative sum's first point sets its baseline, and the
	// metrics no stream takes are left alone.
	if r1.Events != 3 || r1.Rejected != 0 {
		t.Fatalf("first export: %+v", r1)
	}
	if got := rows(t, st, "SELECT http_route, value FROM requests_total"); got != "" {
		t.Errorf("the first cumulative point counted: %s", got)
	}
	send("metrics-2")
	if got := rows(t, st, "SELECT http_route, value FROM requests_total"); got != "/a 15" {
		t.Errorf("after the second export: %s, want 25 - 10", got)
	}
	// The process behind the series restarted: its total starts again from 0.
	send("metrics-3")
	if got := rows(t, st, "SELECT http_route, value FROM requests_total"); got != "/a 18" {
		t.Errorf("after the restart: %s, want 15 + 3", got)
	}
	if got := rows(t, st, "SELECT http_route, value FROM latency_count"); got != "/a 7" {
		t.Errorf("histogram counts: %s", got)
	}
	if got := rows(t, st, "SELECT http_route, value FROM latency_max"); got != "/a 0.2" {
		t.Errorf("histogram max: %s", got)
	}
	if got := rows(t, st, "SELECT queue, w, depth_last FROM queue_win WHERE res = 60 ORDER BY queue, w"); got !=
		"a 1790586000 5; a 1790586060 5; a 1790586120 5; b 1790586000 2.5; b 1790586060 2.5; b 1790586120 2.5" {
		t.Errorf("gauges: %s", got)
	}
	s := in.Stats()
	if s.Baseline != 1 || s.Unused != 9 || s.Rejected != 0 {
		t.Errorf("stats %+v", s)
	}

	// The series' state is in the file: an intake opened on it later goes on from there.
	in2, err := New(eng)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(in2.series); got != 1 {
		t.Errorf("series loaded: %d", got)
	}
	if got := rows(t, st, "SELECT count(*) FROM "+RequestsTable); got != "3" {
		t.Errorf("request digests kept: %s", got)
	}
}

// A series that began after the intake started counts its first point in full: everything in it
// happened while Precomputing was taking the metric.
func TestCumulativeNewSeries(t *testing.T) {
	eng, st, in := open(t, testPolicy, T-1000)
	body := fixture(t, "metrics-1.pb")
	ms, _ := DecodeMetrics(body)
	if _, err := in.Metrics(body, ms); err != nil {
		t.Fatal(err)
	}
	eng.Checkpoint()
	if got := rows(t, st, "SELECT http_route, value FROM requests_total"); got != "/a 10" {
		t.Errorf("a series newer than the intake: %s, want all 10", got)
	}
}

// Points that don't fit the stream are rejected with a reason, and nothing is counted.
func TestIntakeRejects(t *testing.T) {
	src := `stream depths from metrics "queue.depth" {
  key   queue text
  value depth integer
  rollup 1m keep 1d
}
stream hist from metrics "http.server.request.duration" {
  key   http_route text
  key   status integer
  value count integer
  rollup 1m keep 1d
}
stream two from metrics "queue.depth" {
  key   queue text
  value lo real
  value hi real
  rollup 1m keep 1d
}
stream rpc from metrics "rpc.duration" {
  value calls integer
  rollup 1m keep 1d
}`
	_, _, in := open(t, src, T)
	body := fixture(t, "metrics-1.pb")
	ms, _ := DecodeMetrics(body)
	res, err := in.Metrics(body, ms)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 1 { // queue a's 5 is a whole number; queue b's 2.5 is not
		t.Errorf("events %d, want 1", res.Events)
	}
	// Rejected: queue b's 2.5 for an integer value, both gauges for a stream of two values, the
	// histogram for a key it has no attribute for, and the summary.
	if res.Rejected != 5 || !strings.Contains(res.Message, "which fills one value; stream two has 2") {
		t.Errorf("result %+v", res)
	}
}
